//go:build windows

package devkey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func dpapi(data []byte, protect bool) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// openOrCreateDPAPIKey keeps a software key in a file encrypted for the
// current Windows user.
func openOrCreateDPAPIKey(path string) (*ecdsa.PrivateKey, bool, error) {
	blob, err := os.ReadFile(path)
	if err == nil {
		der, err := dpapi(blob, false)
		if err != nil {
			return nil, false, err
		}
		k, err := x509.ParseECPrivateKey(der)
		return k, false, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, false, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, false, err
	}
	if blob, err = dpapi(der, true); err != nil {
		return nil, false, err
	}
	return k, true, os.WriteFile(path, blob, 0o600)
}
