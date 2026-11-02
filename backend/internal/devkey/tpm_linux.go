//go:build linux

package devkey

import (
	"crypto"
	"crypto/ecdsa"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"sync"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/linuxtpm"
)

// The kernel resource manager: every open file has its own handles, so
// several programs can use the TPM at once.
// Tests replace it with a simulator.
var openTPM = func() (transport.TPMCloser, error) { return linuxtpm.Open("/dev/tpmrm0") }

// tpmKey is an ECDSA P-256 key created inside the TPM 2.0 under the
// standard ECC storage key. The file holds only its blob, encrypted by
// the TPM: it loads into this TPM only.
type tpmKey struct {
	mu  sync.Mutex
	tpm transport.TPMCloser
	h   tpm2.NamedHandle
	pub *ecdsa.PublicKey
}

func (k *tpmKey) Kind() string { return "tpm" }

func (k *tpmKey) Public() crypto.PublicKey { return k.pub }

func (k *tpmKey) Sign(_ io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	rsp, err := tpm2.Sign{
		KeyHandle: k.h,
		Digest:    tpm2.TPM2BDigest{Buffer: digest},
		InScheme: tpm2.TPMTSigScheme{
			Scheme:  tpm2.TPMAlgECDSA,
			Details: tpm2.NewTPMUSigScheme(tpm2.TPMAlgECDSA, &tpm2.TPMSSchemeHash{HashAlg: tpm2.TPMAlgSHA256}),
		},
		Validation: tpm2.TPMTTKHashCheck{Tag: tpm2.TPMSTHashCheck},
	}.Execute(k.tpm)
	if err != nil {
		return nil, fmt.Errorf("devkey: TPM sign: %w", err)
	}
	sig, err := rsp.Signature.Signature.ECDSA()
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(struct{ R, S *big.Int }{
		new(big.Int).SetBytes(sig.SignatureR.Buffer),
		new(big.Int).SetBytes(sig.SignatureS.Buffer),
	})
}

var signingTemplate = tpm2.TPMTPublic{
	Type:    tpm2.TPMAlgECC,
	NameAlg: tpm2.TPMAlgSHA256,
	ObjectAttributes: tpm2.TPMAObject{
		FixedTPM:            true,
		FixedParent:         true,
		SensitiveDataOrigin: true,
		UserWithAuth:        true,
		SignEncrypt:         true,
	},
	Parameters: tpm2.NewTPMUPublicParms(tpm2.TPMAlgECC, &tpm2.TPMSECCParms{
		CurveID: tpm2.TPMECCNistP256,
		Scheme: tpm2.TPMTECCScheme{
			Scheme:  tpm2.TPMAlgECDSA,
			Details: tpm2.NewTPMUAsymScheme(tpm2.TPMAlgECDSA, &tpm2.TPMSSigSchemeECDSA{HashAlg: tpm2.TPMAlgSHA256}),
		},
	}),
}

// srk recreates the storage key: the same template gives the same key on
// the same TPM, so nothing has to be kept in its NV memory.
func srk(tpm transport.TPM) (tpm2.NamedHandle, error) {
	rsp, err := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHOwner,
		InPublic:      tpm2.New2B(tpm2.ECCSRKTemplate),
	}.Execute(tpm)
	if err != nil {
		return tpm2.NamedHandle{}, fmt.Errorf("devkey: TPM storage key: %w", err)
	}
	return tpm2.NamedHandle{Handle: rsp.ObjectHandle, Name: rsp.Name}, nil
}

func flush(tpm transport.TPM, h tpm2.NamedHandle) {
	tpm2.FlushContext{FlushHandle: h.Handle}.Execute(tpm)
}

type tpmBlob struct{ Public, Private []byte }

func createTPMKey(path string) (Key, error) {
	tpm, err := openTPM()
	if err != nil {
		return nil, err
	}
	parent, err := srk(tpm)
	if err != nil {
		tpm.Close()
		return nil, err
	}
	defer flush(tpm, parent)
	rsp, err := tpm2.Create{
		ParentHandle: tpm2.AuthHandle{Handle: parent.Handle, Name: parent.Name, Auth: tpm2.PasswordAuth(nil)},
		InPublic:     tpm2.New2B(signingTemplate),
	}.Execute(tpm)
	if err != nil {
		tpm.Close()
		return nil, fmt.Errorf("devkey: TPM create: %w", err)
	}
	blob, err := asn1.Marshal(tpmBlob{tpm2.Marshal(rsp.OutPublic), tpm2.Marshal(rsp.OutPrivate)})
	if err == nil {
		err = os.WriteFile(path, blob, 0o600)
	}
	if err != nil {
		tpm.Close()
		return nil, err
	}
	return loadTPMKey(tpm, parent, rsp.OutPublic, rsp.OutPrivate)
}

func openTPMKey(path string) (Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var blob tpmBlob
	if _, err := asn1.Unmarshal(b, &blob); err != nil {
		return nil, fmt.Errorf("devkey: %s is damaged", path)
	}
	public, err := tpm2.Unmarshal[tpm2.TPM2BPublic](blob.Public)
	if err != nil {
		return nil, fmt.Errorf("devkey: %s is damaged", path)
	}
	private, err := tpm2.Unmarshal[tpm2.TPM2BPrivate](blob.Private)
	if err != nil {
		return nil, fmt.Errorf("devkey: %s is damaged", path)
	}
	tpm, err := openTPM()
	if err != nil {
		return nil, fmt.Errorf("devkey: TPM: %w", err)
	}
	parent, err := srk(tpm)
	if err != nil {
		tpm.Close()
		return nil, err
	}
	defer flush(tpm, parent)
	return loadTPMKey(tpm, parent, *public, *private)
}

func loadTPMKey(tpm transport.TPMCloser, parent tpm2.NamedHandle, public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate) (Key, error) {
	rsp, err := tpm2.Load{
		ParentHandle: tpm2.AuthHandle{Handle: parent.Handle, Name: parent.Name, Auth: tpm2.PasswordAuth(nil)},
		InPublic:     public,
		InPrivate:    private,
	}.Execute(tpm)
	if err != nil {
		tpm.Close()
		// The blob was made by another TPM: the directory was copied.
		return nil, fmt.Errorf("devkey: the TPM key is not from this computer: %w", err)
	}
	contents, err := public.Contents()
	if err == nil {
		var pub crypto.PublicKey
		if pub, err = tpm2.Pub(*contents); err == nil {
			if ec, ok := pub.(*ecdsa.PublicKey); ok {
				return &tpmKey{tpm: tpm, h: tpm2.NamedHandle{Handle: rsp.ObjectHandle, Name: rsp.Name}, pub: ec}, nil
			}
			err = errors.New("devkey: TPM key is not ECDSA")
		}
	}
	flush(tpm, tpm2.NamedHandle{Handle: rsp.ObjectHandle})
	tpm.Close()
	return nil, err
}
