package devkey

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
)

// secretStore is the system store of secrets: Secret Service on Linux,
// Keychain on macOS. It keeps only the 32-byte key that seals the device
// key file, so the file alone is useless on another computer or account.
type secretStore interface {
	Get(name string) (string, error)
	Set(name, value string) error
}

// errNoSecret means the store has nothing under the name.
var errNoSecret = errors.New("devkey: secret not found")

type sealedKey struct{ *ecdsa.PrivateKey }

func (sealedKey) Kind() string { return "keyring" }

func sealer(store secretStore, name string, create bool) (cipher.AEAD, error) {
	var key []byte
	value, err := store.Get(name)
	switch {
	case err == nil:
		key, err = base64.StdEncoding.DecodeString(value)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("devkey: secret %q is damaged", name)
		}
	case errors.Is(err, errNoSecret) && create:
		key = make([]byte, 32)
		rand.Read(key)
		if err := store.Set(name, base64.StdEncoding.EncodeToString(key)); err != nil {
			return nil, fmt.Errorf("devkey: system secret store: %w", err)
		}
	case errors.Is(err, errNoSecret):
		return nil, fmt.Errorf("devkey: secret %q is not in the system store of this user", name)
	default:
		return nil, fmt.Errorf("devkey: system secret store: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func createSealedKey(store secretStore, name, path string) (Key, error) {
	aead, err := sealer(store, name, true)
	if err != nil {
		return nil, err
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	// The name is bound in as additional data: a sealed file does not open
	// with the secret of another key.
	out := aead.Seal(nonce, nonce, der, []byte(name))
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return nil, err
	}
	return sealedKey{k}, nil
}

func openSealedKey(store secretStore, name, path string) (Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	aead, err := sealer(store, name, false)
	if err != nil {
		return nil, err
	}
	if len(b) < aead.NonceSize() {
		return nil, fmt.Errorf("devkey: %s is damaged", path)
	}
	der, err := aead.Open(nil, b[:aead.NonceSize()], b[aead.NonceSize():], []byte(name))
	if err != nil {
		return nil, fmt.Errorf("devkey: %s does not open with the secret of this user", path)
	}
	k, err := x509.ParseECPrivateKey(der)
	if err != nil {
		return nil, err
	}
	return sealedKey{k}, nil
}
