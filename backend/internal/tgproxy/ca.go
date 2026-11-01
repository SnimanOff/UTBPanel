package tgproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// TelegramHost is the only name the interception CA may sign.
const TelegramHost = "api.telegram.org"

// CA is the interception authority of the core. Its Name Constraints
// permit only api.telegram.org: even a leaked key cannot sign another site.
type CA struct {
	Cert *x509.Certificate
	key  *ecdsa.PrivateKey
	leaf *tls.Certificate
	PEM  []byte
}

// LoadCA reads the CA from dir or creates it on first start.
func LoadCA(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certPath, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	ca := &CA{}
	certPEM, err1 := os.ReadFile(certPath)
	keyPEM, err2 := os.ReadFile(keyPath)
	if err1 == nil && err2 == nil {
		cb, _ := pem.Decode(certPEM)
		kb, _ := pem.Decode(keyPEM)
		if cb == nil || kb == nil {
			return nil, errors.New("tgproxy: damaged CA files")
		}
		c, err := x509.ParseCertificate(cb.Bytes)
		if err != nil {
			return nil, err
		}
		k, err := x509.ParseECPrivateKey(kb.Bytes)
		if err != nil {
			return nil, err
		}
		ca.Cert, ca.key, ca.PEM = c, k, certPEM
	} else {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		tpl := &x509.Certificate{
			SerialNumber:                big.NewInt(time.Now().UnixNano()),
			Subject:                     pkix.Name{CommonName: "TG Panel Bot API interception"},
			NotBefore:                   time.Now().Add(-time.Hour),
			NotAfter:                    time.Now().Add(20 * 365 * 24 * time.Hour),
			IsCA:                        true,
			BasicConstraintsValid:       true,
			MaxPathLenZero:              true,
			KeyUsage:                    x509.KeyUsageCertSign,
			PermittedDNSDomainsCritical: true,
			PermittedDNSDomains:         []string{TelegramHost},
		}
		der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
		if err != nil {
			return nil, err
		}
		c, _ := x509.ParseCertificate(der)
		kder, _ := x509.MarshalECPrivateKey(k)
		certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
			return nil, err
		}
		ca.Cert, ca.key, ca.PEM = c, k, certPEM
	}
	return ca, ca.makeLeaf()
}

// makeLeaf issues the certificate for api.telegram.org.
func (ca *CA) makeLeaf() error {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: TelegramHost},
		DNSNames:     []string{TelegramHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, &k.PublicKey, ca.key)
	if err != nil {
		return err
	}
	ca.leaf = &tls.Certificate{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: k}
	return nil
}

// Sign issues a leaf for any name; only tests use it to check that the Name
// Constraints work.
func (ca *CA) Sign(name string) (*tls.Certificate, error) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, &k.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: k}, nil
}

// systemRoots are where Linux distributions keep the trusted roots.
var systemRoots = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/cert.pem",
}

// WriteBundle writes system roots plus the interception CA; bots get its
// path in SSL_CERT_FILE (which replaces the system roots in Go).
func (ca *CA) WriteBundle(path string) error {
	var bundle []byte
	for _, p := range systemRoots {
		if b, err := os.ReadFile(p); err == nil {
			bundle = append(bundle, b...)
			bundle = append(bundle, '\n')
			break
		}
	}
	bundle = append(bundle, ca.PEM...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bundle, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
