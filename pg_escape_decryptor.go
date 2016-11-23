package acra

import (
	"bytes"
	"encoding/binary"
	. "github.com/cossacklabs/acra/utils"
	"io"
	"log"
	"strconv"

	"fmt"
	"github.com/cossacklabs/themis/gothemis/cell"
	"github.com/cossacklabs/themis/gothemis/keys"
	"github.com/cossacklabs/themis/gothemis/message"
)

// TAG_BEGIN in hex format
// 133              32   251
//[92, 50, 48, 53], 32, [92, 51, 55, 51]
var ESCAPE_TAG_BEGIN = []byte{92, 50, 48, 53, 32, 92, 51, 55, 51}


func EncodeToOctal(from, to []byte) int {
	output_length := 0
	for _, c := range from {
		if IsPrintableEscapeChar(c) {
			if c == SLASH_CHAR {
				to = append(to, []byte{SLASH_CHAR, SLASH_CHAR}...)
				output_length += 2
			} else {
				to = append(to, c)
				output_length++
			}
		} else {
			to = append(to, SLASH_CHAR)
			octal := strconv.FormatInt(int64(c), 8)
			switch len(octal) {
			case 3:
				to = append(to, []byte(octal)...)
			case 2:
				to = append(to, '0', octal[0], octal[1])

			case 1:
				to = append(to, '0', '0', octal[0])
			}
			output_length += 4
		}
	}
	return output_length
}

type PgEscapeDecryptor struct {
	current_index    uint8
	output_size      int
	is_with_zone     bool
	poison_key       []byte
	callback_storage *PoisonCallbackStorage
	// max size can be 4 characters for octal representation per byte
	oct_key_block_buffer     [KEY_BLOCK_LENGTH * 4]byte
	decoded_key_block_buffer []byte
	//uint64
	length_buf [8]byte
	// 4 oct symbols (\000) ber byte
	oct_length_buf [8 * 4]byte
	oct_char_buf   [3]byte
	key_store      KeyStore
	zone_matcher   *ZoneIdMatcher
}

func NewPgEscapeDecryptor() *PgEscapeDecryptor {
	return &PgEscapeDecryptor{
		current_index:            0,
		is_with_zone:             false,
		output_size:              0,
		decoded_key_block_buffer: make([]byte, KEY_BLOCK_LENGTH),
	}
}

func (decryptor *PgEscapeDecryptor) SetWithZone(b bool) {
	decryptor.is_with_zone = b
}

func (decryptor *PgEscapeDecryptor) SetPoisonKey(key []byte) {
	decryptor.poison_key = key
}

func (decryptor *PgEscapeDecryptor) GetPoisonKey() []byte {
	return decryptor.poison_key
}

func (decryptor *PgEscapeDecryptor) MatchBeginTag(char byte) bool {
	if char == ESCAPE_TAG_BEGIN[decryptor.current_index] {
		decryptor.current_index++
		decryptor.output_size++
		return true
	} else {
		return false
	}

}
func (decryptor *PgEscapeDecryptor) IsMatched() bool {
	return int(decryptor.current_index) == len(ESCAPE_TAG_BEGIN)
}
func (decryptor *PgEscapeDecryptor) Reset() {
	decryptor.current_index = 0
	decryptor.output_size = 0
}
func (decryptor *PgEscapeDecryptor) GetMatched() []byte {
	return ESCAPE_TAG_BEGIN[:decryptor.current_index]
}

func (decryptor *PgEscapeDecryptor) readOctalData(data, oct_data []byte, reader io.Reader) (int, int, error) {
	data_index := 0
	oct_data_index := 0
	var char_buf [1]byte
	for {
		n, err := reader.Read(char_buf[:])
		if err != nil {
			return data_index, oct_data_index, err
		}
		if n != 1 {
			log.Println("Debug: readOctalData read 0 bytes")
			return data_index, oct_data_index, FAKE_ACRA_STRUCT
		}
		oct_data[oct_data_index] = char_buf[0]
		oct_data_index++
		if !IsPrintableEscapeChar(char_buf[0]) {
			return data_index, oct_data_index, FAKE_ACRA_STRUCT
		}

		// if slash than next char must be slash too
		if char_buf[0] == SLASH_CHAR {
			// read next char
			_, err := reader.Read(char_buf[:])
			if err != nil {
				return data_index, oct_data_index, err
			}
			oct_data[oct_data_index] = char_buf[0]
			oct_data_index++
			if char_buf[0] == SLASH_CHAR {
				// just write slash char
				data[data_index] = char_buf[0]
				data_index++
			} else {
				decryptor.oct_char_buf[0] = char_buf[0]
				// read next 3 oct bytes
				n, err := io.ReadFull(reader, decryptor.oct_char_buf[1:])
				if err != nil {
					return data_index, oct_data_index, err
				}
				if n != len(decryptor.oct_char_buf)-1 {
					if n != 0 {
						copy(oct_data[oct_data_index:oct_data_index+n], decryptor.oct_char_buf[1:1+n])
						oct_data_index += n
					}
					log.Printf("Warning: expected 2 octal symbols, but read %v\n", n)
					return data_index, oct_data_index, FAKE_ACRA_STRUCT
				}
				// parse 3 octal symbols
				num, err := strconv.ParseInt(string(decryptor.oct_char_buf[:]), 8, 9)
				if err != nil {
					return data_index, oct_data_index, FAKE_ACRA_STRUCT
				}
				data[data_index] = byte(num)
				data_index++

				copy(oct_data[oct_data_index:oct_data_index+len(decryptor.oct_char_buf)-1], decryptor.oct_char_buf[1:])
				oct_data_index += 2
			}
		} else {
			// just write to data
			data[data_index] = char_buf[0]
			data_index++
		}
		if data_index == cap(data) {
			return data_index, oct_data_index, nil
		}
	}
}
func (decryptor *PgEscapeDecryptor) ReadSymmetricKey(private_key *keys.PrivateKey, reader io.Reader) ([]byte, []byte, error) {
	data_length, oct_data_length, err := decryptor.readOctalData(decryptor.decoded_key_block_buffer, decryptor.oct_key_block_buffer[:], reader)
	if err != nil {
		return nil, decryptor.oct_key_block_buffer[:oct_data_length], err
	}
	if len(decryptor.decoded_key_block_buffer) != KEY_BLOCK_LENGTH || data_length != KEY_BLOCK_LENGTH {
		return nil, decryptor.oct_key_block_buffer[:oct_data_length], FAKE_ACRA_STRUCT
	}
	smessage := message.New(private_key, &keys.PublicKey{Value: decryptor.decoded_key_block_buffer[:PUBLIC_KEY_LENGTH]})
	symmetric_key, err := smessage.Unwrap(decryptor.decoded_key_block_buffer[PUBLIC_KEY_LENGTH:])
	if err != nil {
		log.Printf("Warning: %v\n", ErrorMessage("can't unwrap scell data", err))
		return nil, decryptor.oct_key_block_buffer[:oct_data_length], FAKE_ACRA_STRUCT
	}
	decryptor.output_size += oct_data_length
	return symmetric_key, decryptor.oct_key_block_buffer[:oct_data_length], nil
}

func (decryptor *PgEscapeDecryptor) readDataLength(reader io.Reader) (uint64, []byte, error) {
	var length uint64

	len_count, oct_len_count, err := decryptor.readOctalData(decryptor.length_buf[:], decryptor.oct_length_buf[:], reader)
	if err != nil {
		log.Printf("Warning: %v\n", ErrorMessage("can't read data length", err))
		return 0, decryptor.oct_length_buf[:oct_len_count], err
	}
	if len_count != len(decryptor.length_buf) {
		log.Printf("Warning: incorrect length count, %v!=%v\n", len_count, len(decryptor.length_buf))
		return 0, decryptor.oct_length_buf[:oct_len_count], FAKE_ACRA_STRUCT
	}
	decryptor.output_size += oct_len_count
	binary.Read(bytes.NewBuffer(decryptor.length_buf[:]), binary.LittleEndian, &length)
	return length, decryptor.oct_length_buf[:oct_len_count], nil
}
func (decryptor *PgEscapeDecryptor) readScellData(length uint64, reader io.Reader) ([]byte, []byte, error) {
	hex_buf := make([]byte, int(length)*4)
	buf := make([]byte, int(length))
	n, oct_n, err := decryptor.readOctalData(buf, hex_buf, reader)
	if err != nil {
		log.Printf("Warning: %v\n", ErrorMessage(fmt.Sprintf("can't read scell data with passed length=%v", length), err))
		return nil, hex_buf[:oct_n], err
	}
	if n != int(length) {
		log.Printf("Warning: read incorrect length, %v!=%v\n", n, length)
		return nil, hex_buf[:oct_n], FAKE_ACRA_STRUCT
	}
	decryptor.output_size += oct_n
	return buf, hex_buf[:oct_n], nil
}

func (decryptor *PgEscapeDecryptor) getFullDataLength() int {
	return decryptor.output_size
}

func (decryptor *PgEscapeDecryptor) ReadData(symmetric_key, zone_id []byte, reader io.Reader) ([]byte, error) {
	length, hex_length_buf, err := decryptor.readDataLength(reader)
	if err != nil {
		return hex_length_buf, err
	}
	data, oct_data, err := decryptor.readScellData(length, reader)
	if err != nil {
		return append(hex_length_buf, oct_data...), err
	}

	scell := cell.New(symmetric_key, cell.CELL_MODE_SEAL)
	decrypted, err := scell.Unprotect(data, nil, zone_id)
	// fill zero symmetric_key
	FillSlice(byte(0), symmetric_key[:])
	if err != nil {
		return append(hex_length_buf, oct_data...), FAKE_ACRA_STRUCT
	}

	// 16 - uint64 length in escape encoded format
	output := make([]byte, len(decrypted)*4)
	n_data := EncodeToOctal(decrypted, output[:0])
	return output[:n_data], nil
}

func (decryptor *PgEscapeDecryptor) SetKeyStore(store KeyStore) {
	decryptor.key_store = store
}

func (decryptor *PgEscapeDecryptor) GetPrivateKey() (*keys.PrivateKey, error) {
	return decryptor.key_store.GetKey(decryptor.GetMatchedZoneId())
}

func (decryptor *PgEscapeDecryptor) SetZoneMatcher(zone_matcher *ZoneIdMatcher) {
	decryptor.zone_matcher = zone_matcher
}

func (decryptor *PgEscapeDecryptor) MatchZone(c byte) bool {
	return decryptor.zone_matcher.Match(c)
}

func (decryptor *PgEscapeDecryptor) IsWithZone() bool {
	return decryptor.is_with_zone
}

func (decryptor *PgEscapeDecryptor) IsMatchedZone() bool {
	return decryptor.zone_matcher.IsMatched()
}

func (decryptor *PgEscapeDecryptor) ResetZoneMatch() {
	decryptor.zone_matcher.Reset()
}

func (decryptor *PgEscapeDecryptor) GetMatchedZoneId() []byte {
	if decryptor.IsWithZone() {
		return decryptor.zone_matcher.GetZoneId()
	} else {
		return nil
	}
}

func (decryptor *PgEscapeDecryptor) SetPoisonCallbackStorage(storage *PoisonCallbackStorage) {
	decryptor.callback_storage = storage
}

func (decryptor *PgEscapeDecryptor) GetPoisonCallbackStorage() *PoisonCallbackStorage {
	return decryptor.callback_storage
}