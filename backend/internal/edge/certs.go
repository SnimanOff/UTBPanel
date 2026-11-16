package edge

import (
	"context"
	"crypto/tls"

	"github.com/SnimanOff/UTBPanel/backend/internal/safe"
)

// Let's Encrypt certificates are issued lazily by autocert, inside the TLS
// handshake. That is too late for a real visitor: the first request pays for
// the whole ACME exchange (up to five minutes), and when it fails the browser
// shows a TLS error instead of a page. The hosts from the settings are
// therefore asked for ahead of time, and a new page or Mini App is warmed up
// as soon as it appears.

// warmAllCerts asks for a certificate for every configured host.
func (e *Edge) warmAllCerts(ctx context.Context) {
	for _, host := range e.settings.hosts() {
		host := host
		safe.Go(e.Log, "certificate warm-up", func() { e.warmCert(ctx, host) })
	}
}

// warmNewCerts starts issuance for hosts that appeared in a settings update.
func (e *Edge) warmNewCerts(before map[string]bool) {
	for _, host := range e.settings.hosts() {
		if before[host] {
			continue
		}
		host := host
		safe.Go(e.Log, "certificate warm-up", func() { e.warmCert(context.Background(), host) })
	}
}

// warmCert runs one issuance ahead of time. warmSem bounds how many run at
// once: the edge is small and must keep serving webhooks meanwhile.
func (e *Edge) warmCert(ctx context.Context, host string) {
	acm := acmeManager.Load()
	if acm == nil || host == "" || e.warmSem == nil || e.certHost(ctx, host) != nil {
		return
	}
	select {
	case e.warmSem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-e.warmSem }()
	// autocert selects its RSA cache entry when the hello offers no ECDSA
	// cipher suite. Warm the ECDSA entry that modern browsers use.
	hello := &tls.ClientHelloInfo{
		ServerName:   host,
		CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
	}
	if _, err := acm.GetCertificate(hello); err != nil {
		e.Log.Warn("certificate warm-up failed", "host", host, "err", err)
		return
	}
	e.Log.Info("certificate ready", "host", host)
}
