package gate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http/httptest"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
)

// The panel's server certificate must pass the pinning check.
func TestPinnedVerifyAcceptsServerCertificate(t *testing.T) {
	p := newPanel(t)
	cert, _, err := p.ca.ServerCert("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := pinnedVerify(p.ca.Fingerprint())([][]byte{cert.Raw, p.ca.Cert().Raw}, nil); err != nil {
		t.Fatalf("server certificate rejected: %v", err)
	}
}

// The same CA signs device certificates. One of them must never be taken for
// the panel: it is a client certificate, and a revoked key must not be able to
// impersonate the server either (the serial registry is not consulted here).
func TestPinnedVerifyRejectsDeviceCertificate(t *testing.T) {
	p := newPanel(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := p.ca.Issue(context.Background(), pki.Child, "laptop", "", &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ca.Revoke(context.Background(), pki.SerialString(dev.SerialNumber)); err != nil {
		t.Fatal(err)
	}
	err = pinnedVerify(p.ca.Fingerprint())([][]byte{dev.Raw, p.ca.Cert().Raw}, nil)
	if err == nil {
		t.Fatal("a device certificate was accepted as the server")
	}
}

// A CA certificate itself is not a server certificate.
func TestPinnedVerifyRejectsCACertificate(t *testing.T) {
	p := newPanel(t)
	err := pinnedVerify(p.ca.Fingerprint())([][]byte{p.ca.Cert().Raw, p.ca.Cert().Raw}, nil)
	if err == nil {
		t.Fatal("the CA certificate was accepted as the server")
	}
}

// Only the gate's own origin may change the panel address.
func TestSameOrigin(t *testing.T) {
	g := &Gate{port: "47100"}
	cases := []struct {
		name, origin, site string
		want               bool
	}{
		{"no headers", "", "", true},
		{"own origin", "http://127.0.0.1:47100", "", true},
		{"localhost origin", "http://localhost:47100", "", true},
		{"foreign origin", "http://evil.test", "", false},
		{"other port", "http://127.0.0.1:8080", "", false},
		{"cross-site fetch", "", "cross-site", false},
		{"same-origin fetch", "", "same-origin", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/__gate/server", nil)
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.site != "" {
				r.Header.Set("Sec-Fetch-Site", c.site)
			}
			if got := g.sameOrigin(r); got != c.want {
				t.Fatalf("sameOrigin = %v, want %v", got, c.want)
			}
		})
	}
}
