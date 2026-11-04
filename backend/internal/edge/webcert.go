package edge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"sync/atomic"

	"golang.org/x/crypto/acme/autocert"

	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// MethodWebCert gives the panel the domain certificate for its passkey
// listener: Let's Encrypt validates the domain here, on :80.
const MethodWebCert = "tls.cert"

// WebCert is the certificate chain and key in PEM.
type WebCert struct {
	Chain []byte `cbor:"1,keyasint"`
	Key   []byte `cbor:"2,keyasint"`
}

var acmeManager atomic.Pointer[autocert.Manager]

func (e *Edge) webCertMux() *wire.Mux {
	m := wire.NewMux()
	m.Handle(MethodWebCert, wire.Func(func(ctx context.Context, _ *wire.Conn, _ struct{}) (WebCert, error) {
		acm := acmeManager.Load()
		if acm == nil || e.Cfg.Domain == "" {
			return WebCert{}, wire.Errorf(wire.CodeNotFound, "the edge has no Let's Encrypt certificate: set DOMAIN")
		}
		cert, err := acm.GetCertificate(&tls.ClientHelloInfo{ServerName: e.Cfg.Domain})
		if err != nil {
			return WebCert{}, wire.Errorf(wire.CodeUnavailable, "certificate: %v", err)
		}
		var out WebCert
		for _, der := range cert.Certificate {
			out.Chain = append(out.Chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
		}
		key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
		if err != nil {
			return WebCert{}, err
		}
		out.Key = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
		return out, nil
	}))
	return m
}
