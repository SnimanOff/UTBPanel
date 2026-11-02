//go:build darwin

package devkey

import (
	"fmt"
	"path/filepath"
)

// On macOS the key file is sealed with a secret from the login Keychain.
// The Secure Enclave needs a signed binary with a keychain entitlement and
// cgo; until gate is built that way, noTPM changes nothing here.
func create(dir, name string, _ bool) (Key, string, error) {
	k, err := createSealedKey(keyStore, name, filepath.Join(dir, "key.sealed"))
	if err != nil {
		return nil, "", err
	}
	return k, "keyring:" + name, nil
}

func openRef(dir, kind, arg string) (Key, error) {
	switch kind {
	case "keyring":
		return openSealedKey(keyStore, arg, filepath.Join(dir, "key.sealed"))
	case "file":
		return openFileKey(filepath.Join(dir, arg))
	}
	return nil, fmt.Errorf("devkey: %s keys are not supported on this system", kind)
}
