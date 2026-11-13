package edge

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// selfSigned loads or creates the certificate for :8443 when there is no
// domain. Telegram accepts it when the core passes its PEM in setWebhook
// (parameter certificate).
func selfSigned(dir, addr string) (*tls.Certificate, []byte, error) {
	certPath, keyPath := filepath.Join(dir, "selfsigned.crt"), filepath.Join(dir, "selfsigned.key")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		leaf, _ := x509.ParseCertificate(c.Certificate[0])
		if leaf != nil && time.Until(leaf.NotAfter) > 30*24*time.Hour && leaf.VerifyHostname(addr) == nil {
			pemBytes, _ := os.ReadFile(certPath)
			return &c, pemBytes, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: addr},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(addr); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{addr}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, nil, err
	}
	c, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
	return &c, certPEM, err
}

// tlsSetup returns the :8443 TLS config, the self-signed PEM (nil with a
// domain), and the autocert manager (nil without a domain).
func (e *Edge) tlsSetup() (*tls.Config, []byte, *autocert.Manager, error) {
	if e.Cfg.Domain != "" {
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: e.certHost, // the domain and bots' Mini App hosts
			Cache:      autocert.DirCache(filepath.Join(e.Cfg.DataDir, "certs")),
		}
		cfg := m.TLSConfig()
		cfg.MinVersion = tls.VersionTLS12 // Telegram's servers
		return cfg, nil, m, nil
	}
	if e.Cfg.PublicAddr == "" {
		return nil, nil, nil, errors.New("edge: set TGPANEL_PUBLIC_ADDR (the IP Telegram sends webhooks to) or TGPANEL_DOMAIN")
	}
	cert, pemBytes, err := selfSigned(filepath.Join(e.Cfg.DataDir, "certs"), e.Cfg.PublicAddr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("edge: self-signed certificate: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*cert}}, pemBytes, nil, nil
}
