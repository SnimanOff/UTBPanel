//go:build linux && cgo && tpmsim

package devkey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"path/filepath"
	"testing"

	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/simulator"
)

// shared keeps one simulator for several opens, as the kernel keeps one TPM.
type shared struct{ transport.TPMCloser }

func (shared) Close() error { return nil }

// useSimulator starts a TPM with fresh seeds: another computer.
func useSimulator(t *testing.T) transport.TPMCloser {
	t.Helper()
	sim, err := simulator.OpenSimulator()
	if err != nil {
		t.Fatal(err)
	}
	openTPM = func() (transport.TPMCloser, error) { return shared{sim}, nil }
	return sim
}

// go test -tags tpmsim ./internal/devkey (Linux, cgo).
func TestTPMKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.tpm")
	sim := useSimulator(t)
	k, err := createTPMKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if k.Kind() != "tpm" {
		t.Fatal(k.Kind())
	}
	reopened, err := openTPMKey(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	for _, key := range []Key{k, reopened} {
		sig, err := key.Sign(rand.Reader, digest[:], crypto.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		if !ecdsa.VerifyASN1(k.Public().(*ecdsa.PublicKey), digest[:], sig) {
			t.Fatal("signature does not verify")
		}
	}
	sim.Close()

	// The same file on another TPM does not load.
	sim = useSimulator(t)
	defer sim.Close()
	if _, err := openTPMKey(path); err == nil {
		t.Fatal("key loaded into another TPM")
	}
}
