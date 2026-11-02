//go:build linux

package devkey

import (
	"fmt"
	"path/filepath"
)

func create(dir, name string, noTPM bool) (Key, string, error) {
	if !noTPM {
		k, err := createTPMKey(filepath.Join(dir, "key.tpm"))
		if err == nil {
			return k, "tpm:key.tpm", nil
		}
		// No TPM or no access to /dev/tpmrm0 (group tss): fall back.
	}
	k, err := createSealedKey(keyStore, name, filepath.Join(dir, "key.sealed"))
	if err != nil {
		return nil, "", fmt.Errorf("%w (без TPM ключ шифруется секретом из GNOME Keyring или KWallet)", err)
	}
	return k, "keyring:" + name, nil
}

func openRef(dir, kind, arg string) (Key, error) {
	switch kind {
	case "tpm":
		return openTPMKey(filepath.Join(dir, arg))
	case "keyring":
		return openSealedKey(keyStore, arg, filepath.Join(dir, "key.sealed"))
	case "file":
		return openFileKey(filepath.Join(dir, arg))
	}
	return nil, fmt.Errorf("devkey: unknown key kind %q", kind)
}
