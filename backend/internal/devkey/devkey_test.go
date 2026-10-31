package devkey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
)

func TestCreateOpenSoftware(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty dir: %v", err)
	}
	k, err := Create(dir, "tgpanel-test", true)
	if err != nil {
		t.Fatal(err)
	}
	if k.Kind() == "tpm" {
		t.Fatal("noTPM ignored")
	}
	k2, err := Open(dir)
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
}
