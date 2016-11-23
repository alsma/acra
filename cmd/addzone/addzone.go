package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/cossacklabs/acra/keystore"
	. "github.com/cossacklabs/acra/utils"
	"io/ioutil"
	"os"
)

func main() {
	output_dir := flag.String("output_dir", keystore.DEFAULT_KEY_DIR_SHORT, "output dir to save public key")
	fs_keystore := flag.Bool("fs", true, "use filesystem key store")
	//verbose := flag.Bool("v", false, "log to stdout")
	flag.Parse()
	//if *verbose{
	//	log.SetOutput(os.Stdout)
	//}
	output, err := AbsPath(*output_dir)
	if err != nil {
		fmt.Printf("Error: %v\n", ErrorMessage("Can't get absolute path for output dir", err))
		os.Exit(1)
	}
	var key_store keystore.KeyStore
	if *fs_keystore {
		key_store = keystore.NewFilesystemKeyStore(output)
	}
	id, public_key, err := key_store.GenerateKey()
	if err != nil {
		fmt.Printf("Error: %v\n", ErrorMessage("can't add zone", err))
		os.Exit(1)
	}
	public_key_path := fmt.Sprintf("%s/%s", output, keystore.GetPublicKeyFilename(id))
	err = ioutil.WriteFile(public_key_path, public_key, 0644)
	if err != nil {
		fmt.Printf("Error: can't save public key at path: %s\n", public_key_path)
		os.Exit(1)
	}
	response := make(map[string]string)
	response["id"] = string(id)
	response["public_key"] = base64.StdEncoding.EncodeToString(public_key)
	json_output, err := json.Marshal(response)
	if err != nil {
		fmt.Printf("Error: %v\n", ErrorMessage("can't encode to json", err))
		os.Exit(1)
	}
	fmt.Println(string(json_output))
}
