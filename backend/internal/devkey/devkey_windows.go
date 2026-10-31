//go:build windows

package devkey

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"path/filepath"
)

func (k *tpmKey) Kind() string { return "tpm" }

type dpapiKey struct{ *ecdsa.PrivateKey }

func (dpapiKey) Kind() string { return "dpapi" }

func create(dir, name string, noTPM bool) (Key, string, error) {
	if !noTPM {
		k, _, err := openOrCreateTPMKey(name)
		if err == nil {
			if k.tryExportPrivate() == nil {
				k.Close()
				return nil, "", fmt.Errorf("devkey: TPM key %q is exportable", name)
			}
			return k, "tpm:" + name, nil
		}
		// No TPM or it refused: fall back to DPAPI.
	}
	k, _, err := openOrCreateDPAPIKey(filepath.Join(dir, "key.dpapi"))
	if err != nil {
		return nil, "", err
	}
	return dpapiKey{k}, "dpapi:key.dpapi", nil
}

func openRef(dir, kind, arg string) (Key, error) {
	switch kind {
	case "tpm":
		k, err := openTPMKey(arg)
		if errors.Is(err, errKeyNotFound) {
			// The reference points to a key this TPM never had: the state
			// directory was copied from another computer.
			return nil, fmt.Errorf("devkey: TPM key %q is not on this computer", arg)
		}
		return k, err
	case "dpapi":
		k, created, err := openOrCreateDPAPIKey(filepath.Join(dir, arg))
		if err != nil {
			return nil, fmt.Errorf("devkey: DPAPI key cannot be opened by this Windows user: %w", err)
		}
		if created {
			return nil, fmt.Errorf("devkey: DPAPI key file %s is missing", arg)
		}
		return dpapiKey{k}, nil
	case "file":
		return openFileKey(filepath.Join(dir, arg))
	}
	return nil, fmt.Errorf("devkey: unknown key kind %q", kind)
}
