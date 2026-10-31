//go:build windows

package devkey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	providerTPM   = "Microsoft Platform Crypto Provider"
	nteBadKeyset  = 0x80090016
	eccPublicP256 = 0x31534345 // BCRYPT_ECDSA_PUBLIC_P256_MAGIC
)

var (
	ncrypt                  = windows.NewLazySystemDLL("ncrypt.dll")
	procOpenStorageProvider = ncrypt.NewProc("NCryptOpenStorageProvider")
	procCreatePersistedKey  = ncrypt.NewProc("NCryptCreatePersistedKey")
	procOpenKey             = ncrypt.NewProc("NCryptOpenKey")
	procSetProperty         = ncrypt.NewProc("NCryptSetProperty")
	procFinalizeKey         = ncrypt.NewProc("NCryptFinalizeKey")
	procExportKey           = ncrypt.NewProc("NCryptExportKey")
	procSignHash            = ncrypt.NewProc("NCryptSignHash")
	procDeleteKey           = ncrypt.NewProc("NCryptDeleteKey")
	procFreeObject          = ncrypt.NewProc("NCryptFreeObject")

	errKeyNotFound = errors.New("key not found")
)

func call(p *windows.LazyProc, args ...uintptr) error {
	r, _, _ := p.Call(args...)
	switch uint32(r) {
	case 0:
		return nil
	case nteBadKeyset:
		return fmt.Errorf("%s: %w", p.Name, errKeyNotFound)
	default:
		return fmt.Errorf("%s: 0x%08x", p.Name, uint32(r))
	}
}

func utf16(s string) uintptr {
	p, _ := windows.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

// tpmKey is an ECDSA P-256 key held by a CNG key storage provider.
type tpmKey struct {
	prov uintptr
	key  uintptr
	pub  *ecdsa.PublicKey
	mu   sync.Mutex
}

// openTPMKey opens an existing key; errKeyNotFound if there is none.
func openTPMKey(name string) (*tpmKey, error) {
	k := &tpmKey{}
	if err := call(procOpenStorageProvider, uintptr(unsafe.Pointer(&k.prov)), utf16(providerTPM), 0); err != nil {
		return nil, err
	}
	err := call(procOpenKey, k.prov, uintptr(unsafe.Pointer(&k.key)), utf16(name), 0, 0)
	if err == nil {
		k.pub, err = k.publicKey()
	}
	if err != nil {
		k.Close()
		return nil, err
	}
	return k, nil
}

// openOrCreateTPMKey opens the named key or creates a non-exportable one.
func openOrCreateTPMKey(name string) (*tpmKey, bool, error) {
	k := &tpmKey{}
	if err := call(procOpenStorageProvider, uintptr(unsafe.Pointer(&k.prov)), utf16(providerTPM), 0); err != nil {
		return nil, false, err
	}
	err := call(procOpenKey, k.prov, uintptr(unsafe.Pointer(&k.key)), utf16(name), 0, 0)
	created := false
	if errors.Is(err, errKeyNotFound) {
		created = true
		err = k.create(name)
	}
	if err == nil {
		k.pub, err = k.publicKey()
	}
	if err != nil {
		k.Close()
		return nil, false, err
	}
	return k, created, nil
}

func (k *tpmKey) create(name string) error {
	if err := call(procCreatePersistedKey, k.prov, uintptr(unsafe.Pointer(&k.key)),
		utf16("ECDSA_P256"), utf16(name), 0, 0); err != nil {
		return err
	}
	// Export policy 0: the private key never leaves the provider.
	policy := uint32(0)
	if err := call(procSetProperty, k.key, utf16("Export Policy"),
		uintptr(unsafe.Pointer(&policy)), 4, 0); err != nil {
		return err
	}
	return call(procFinalizeKey, k.key, 0)
}

func (k *tpmKey) export(blob string) ([]byte, error) {
	var n uint32
	if err := call(procExportKey, k.key, 0, utf16(blob), 0, 0, 0, uintptr(unsafe.Pointer(&n)), 0); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if err := call(procExportKey, k.key, 0, utf16(blob), 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(unsafe.Pointer(&n)), 0); err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func (k *tpmKey) publicKey() (*ecdsa.PublicKey, error) {
	b, err := k.export("ECCPUBLICBLOB")
	if err != nil {
		return nil, err
	}
	if len(b) < 8 || binary.LittleEndian.Uint32(b) != eccPublicP256 {
		return nil, errors.New("unexpected public key blob")
	}
	size := int(binary.LittleEndian.Uint32(b[4:]))
	if len(b) < 8+2*size {
		return nil, errors.New("short public key blob")
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(b[8 : 8+size]),
		Y:     new(big.Int).SetBytes(b[8+size : 8+2*size]),
	}, nil
}

// tryExportPrivate must fail for a key with export policy 0.
func (k *tpmKey) tryExportPrivate() error {
	_, err := k.export("ECCPRIVATEBLOB")
	return err
}

func (k *tpmKey) Public() crypto.PublicKey { return k.pub }

// Sign implements crypto.Signer. CNG returns r||s, TLS expects ASN.1.
func (k *tpmKey) Sign(_ io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	sig := make([]byte, 64)
	var n uint32
	if err := call(procSignHash, k.key, 0, uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)),
		uintptr(unsafe.Pointer(&sig[0])), uintptr(len(sig)), uintptr(unsafe.Pointer(&n)), 0); err != nil {
		return nil, err
	}
	half := int(n) / 2
	return asn1.Marshal(struct{ R, S *big.Int }{
		new(big.Int).SetBytes(sig[:half]),
		new(big.Int).SetBytes(sig[half:n]),
	})
}

// Delete removes the key from the provider and frees the handle.
func (k *tpmKey) Delete() error {
	err := call(procDeleteKey, k.key, 0)
	k.key = 0
	return err
}

func (k *tpmKey) Close() {
	if k.key != 0 {
		procFreeObject.Call(k.key)
		k.key = 0
	}
	if k.prov != 0 {
		procFreeObject.Call(k.prov)
		k.prov = 0
	}
}
