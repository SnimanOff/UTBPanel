package edge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// stagingDirectory is the Let's Encrypt staging CA. It has its own rate
// limits, and its certificates require explicit trust in the test client.
const stagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"

// certWarnEvery is the minimum interval between warnings for the same
// configured name, or for unknown names as a group.
const certWarnEvery = 5 * time.Minute

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
		cacheDir := "certs"
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: e.certHost,
			Email:      e.Cfg.ACMEEmail,
		}
		if e.Cfg.ACMEStaging {
			cacheDir = "certs-staging"
			m.Client = &acme.Client{DirectoryURL: stagingDirectory}
		}
		m.Cache = autocert.DirCache(filepath.Join(e.Cfg.DataDir, cacheDir))
		cfg := m.TLSConfig()
		cfg.MinVersion = tls.VersionTLS12 // Telegram's servers
		// Keep the fallback separate from the certificate that Telegram
		// trusts in self-signed mode so switching modes preserves that pair.
		fallback, _, err := selfSigned(filepath.Join(e.Cfg.DataDir, "certs-fallback"), e.Cfg.Domain)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("edge: fallback certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{*fallback}
		cfg.GetCertificate = e.logCertErrors(cfg.GetCertificate)
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

// logCertErrors reports why no Let's Encrypt certificate was served and lets
// the handshake continue with the fallback certificate: returning (nil, nil)
// makes crypto/tls use Config.Certificates. A tls-alpn-01 token request is
// passed through unchanged — a fallback there would fail the challenge with a
// misleading error.
func (e *Edge) logCertErrors(base func(*tls.ClientHelloInfo) (*tls.Certificate, error)) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		cert, err := base(hello)
		if err == nil {
			return cert, nil
		}
		if wantsALPNToken(hello) {
			return nil, err
		}
		e.warnCert(hello.ServerName, err)
		return nil, nil
	}
}

// warnCert rate-limits warnings for configured names individually and groups
// all unknown names under one key.
func (e *Edge) warnCert(name string, err error) {
	key := strings.ToLower(name)
	if e.certHost(context.Background(), key) != nil {
		key = ""
	}
	e.certWarnMu.Lock()
	now := time.Now()
	if now.Sub(e.certWarnLast[key]) < certWarnEvery {
		e.certWarnMu.Unlock()
		return
	}
	if e.certWarnLast == nil {
		e.certWarnLast = map[string]time.Time{}
	}
	if now.Sub(e.certWarnSweep) >= certWarnEvery {
		for host, at := range e.certWarnLast {
			if now.Sub(at) >= certWarnEvery {
				delete(e.certWarnLast, host)
			}
		}
		e.certWarnSweep = now
	}
	e.certWarnLast[key] = now
	e.certWarnMu.Unlock()
	log := e.Log
	if log == nil {
		log = slog.Default()
	}
	log.Warn("tls: serving the fallback certificate", "name", name, "err", err)
}

// wantsALPNToken reports whether the peer is a CA validating a tls-alpn-01
// challenge (the same check as acme/autocert's wantsTokenCert).
func wantsALPNToken(hello *tls.ClientHelloInfo) bool {
	return len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == acme.ALPNProto
}
