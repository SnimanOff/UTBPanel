//go:build linux || darwin

package devkey

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const keyringService = "tgpanel-gate"

// systemStore is Secret Service (GNOME Keyring, KWallet) on Linux and the
// login Keychain on macOS.
type systemStore struct{}

func (systemStore) Get(name string) (string, error) {
	v, err := keyring.Get(keyringService, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errNoSecret
	}
	return v, err
}

func (systemStore) Set(name, value string) error { return keyring.Set(keyringService, name, value) }

// keyStore is replaced in tests.
var keyStore secretStore = systemStore{}
