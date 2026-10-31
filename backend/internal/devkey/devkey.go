// Package devkey keeps the device key of gate.exe (plan 2.2): an ECDSA
// P-256 key created on this computer that never leaves it. On Windows it
// lives in the TPM (Microsoft Platform Crypto Provider); without a TPM it
// is a software key encrypted with DPAPI for the current Windows user.
//
// The state directory holds only a reference to the key, so a copied
// directory is useless on another computer.
package devkey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Key is a device key.
type Key interface {
	crypto.Signer
	// Kind is "tpm", "dpapi" or "file".
	Kind() string
}

const refFile = "key.ref"

// ErrNoKey means the directory has no key yet.
var ErrNoKey = errors.New("devkey: no key")

// Open loads the key referenced in dir.
func Open(dir string) (Key, error) {
	b, err := os.ReadFile(filepath.Join(dir, refFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, err
	}
	kind, arg, _ := strings.Cut(strings.TrimSpace(string(b)), ":")
	return openRef(dir, kind, arg)
}

// Create makes a new key for dir. name identifies a TPM key and should
// include the server address and key name. With noTPM a software key is
// used even when a TPM is present.
func Create(dir, name string, noTPM bool) (Key, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	k, ref, err := create(dir, name, noTPM)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, refFile), []byte(ref+"\n"), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// fileKey is a plain PEM key file, used where neither TPM nor DPAPI exist
// (tests, and other systems until the gate supports them).
type fileKey struct{ *ecdsa.PrivateKey }

func (fileKey) Kind() string { return "file" }

func createFileKey(path string) (Key, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return fileKey{k}, nil
}

func openFileKey(path string) (Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("devkey: %s is not PEM", path)
	}
	k, err := x509.ParseECPrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	return fileKey{k}, nil
}
