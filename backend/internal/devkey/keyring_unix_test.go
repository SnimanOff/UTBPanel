//go:build linux || darwin

package devkey

import (
	"os"
	"testing"
)

// Tests do not touch the Keychain or Secret Service of the developer.
func TestMain(m *testing.M) {
	keyStore = memStore{}
	os.Exit(m.Run())
}
