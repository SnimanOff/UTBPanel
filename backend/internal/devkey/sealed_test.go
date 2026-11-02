package devkey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

type memStore map[string]string

func (m memStore) Get(name string) (string, error) {
	v, ok := m[name]
	if !ok {
		return "", errNoSecret
	}
	return v, nil
}

func (m memStore) Set(name, value string) error { m[name] = value; return nil }

func TestSealedKey(t *testing.T) {
	store := memStore{}
	path := filepath.Join(t.TempDir(), "key.sealed")
	k, err := createSealedKey(store, "tgpanel-a", path)
	if err != nil {
		t.Fatal(err)
	}
	if k.Kind() != "keyring" {
		t.Fatal(k.Kind())
	}
	k2, err := openSealedKey(store, "tgpanel-a", path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	sig, err := k2.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.VerifyASN1(k.Public().(*ecdsa.PublicKey), digest[:], sig) {
		t.Fatal("reopened key differs")
	}

	// Another user or computer: the file without its secret.
	if _, err := openSealedKey(memStore{}, "tgpanel-a", path); err == nil {
		t.Fatal("opened without the secret")
	}
	// Another key's secret.
	if _, err := createSealedKey(store, "tgpanel-b", filepath.Join(t.TempDir(), "b")); err != nil {
		t.Fatal(err)
	}
	if _, err := openSealedKey(store, "tgpanel-b", path); err == nil {
		t.Fatal("opened with another secret")
	}
	// A damaged file.
	b, _ := os.ReadFile(path)
	b[len(b)-1] ^= 1
	os.WriteFile(path, b, 0o600)
	if _, err := openSealedKey(store, "tgpanel-a", path); err == nil {
		t.Fatal("damaged file opened")
	}
	store["tgpanel-a"] = "short"
	if _, err := openSealedKey(store, "tgpanel-a", path); err == nil {
		t.Fatal("damaged secret accepted")
	}
}
