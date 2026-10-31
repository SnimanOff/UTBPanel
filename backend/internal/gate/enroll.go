package gate

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/devkey"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
)

type enrollStatus struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	Code  string `json:"code"`
	Cert  []byte `json:"cert,omitempty"`
	CA    []byte `json:"ca,omitempty"`
}

// tpmName names the device key in the TPM: per server, key name and issued
// gate, so one computer can hold keys for several panels.
func (g *Gate) tpmName() string {
	h := sha256.Sum256([]byte(g.cfg.Block.Servers[0] + "|" + g.cfg.Block.KeyName + "|" + hex.EncodeToString(g.cfg.Block.EnrollCert)))
	return "tgpanel-" + g.cfg.Block.KeyName + "-" + hex.EncodeToString(h[:6])
}

func (g *Gate) setEnroll(f func(*EnrollState)) {
	g.mu.Lock()
	f(&g.enroll)
	g.mu.Unlock()
}

// EnrollState returns the current registration state.
func (g *Gate) EnrollState() EnrollState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.enroll
}

// Enroll creates the device key, asks the panel for a certificate with the
// one-off enrollment certificate and waits until the owner confirms the code.
// Network errors are retried every 10 s; a rejected or expired request ends
// the registration.
func (g *Gate) Enroll(ctx context.Context) error {
	for {
		err := g.enrollOnce(ctx)
		if err == nil || ctx.Err() != nil {
			return err
		}
		final := false
		g.setEnroll(func(s *EnrollState) {
			final = s.State == "rejected" || s.State == "expired"
			if !final {
				s.State = "error"
			}
			s.Error = err.Error()
		})
		if final {
			return err
		}
		g.log.Warn("registration failed, retrying", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * g.cfg.PollInterval / 3):
		}
	}
}

func (g *Gate) enrollOnce(ctx context.Context) error {
	key, err := devkey.Open(g.dir)
	if errors.Is(err, devkey.ErrNoKey) {
		key, err = devkey.Create(g.dir, g.tpmName(), g.cfg.NoTPM)
	}
	if err != nil {
		return fmt.Errorf("device key: %w", err)
	}
	g.log.Info("device key ready", "kind", key.Kind())
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: g.cfg.Block.KeyName},
	}, key)
	if err != nil {
		return fmt.Errorf("csr: %w", err)
	}
	enrollKey, err := x509.ParsePKCS8PrivateKey(g.cfg.Block.EnrollKey)
	if err != nil {
		return fmt.Errorf("enrollment key: %w", err)
	}
	enrollCert := &tls.Certificate{Certificate: [][]byte{g.cfg.Block.EnrollCert}, PrivateKey: enrollKey}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: g.tlsConfig(enrollCert)}}
	defer client.CloseIdleConnections()
	base := "https://" + g.Server()

	body, _ := json.Marshal(map[string][]byte{"csr": csr})
	var st enrollStatus
	if err := doJSON(ctx, client, http.MethodPost, base+"/enroll", body, &st); err != nil {
		return err
	}
	g.setEnroll(func(s *EnrollState) { s.State, s.Code, s.Error = "waiting", st.Code, "" })
	g.log.Info("waiting for approval in the panel", "code", st.Code)

	t := time.NewTicker(g.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		if err := doJSON(ctx, client, http.MethodGet, fmt.Sprintf("%s/enroll/%d", base, st.ID), nil, &st); err != nil {
			g.log.Warn("enrollment status", "err", err)
			continue
		}
		switch st.State {
		case pki.DevicePending:
			continue
		case pki.DeviceApproved:
			return g.saveDevice(key, st)
		case pki.DeviceRejected, pki.DeviceExpired:
			g.setEnroll(func(s *EnrollState) { s.State = st.State })
			return fmt.Errorf("registration %s in the panel", st.State)
		}
	}
}

func (g *Gate) saveDevice(key devkey.Key, st enrollStatus) error {
	leaf, err := x509.ParseCertificate(st.Cert)
	if err != nil {
		return err
	}
	pub, _ := x509.MarshalPKIXPublicKey(key.Public())
	leafPub, _ := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if !bytes.Equal(pub, leafPub) {
		return errors.New("panel returned a certificate for another key")
	}
	write := func(name string, der []byte) error {
		return os.WriteFile(filepath.Join(g.dir, name), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	}
	if err := write("device.crt", st.Cert); err != nil {
		return err
	}
	if len(st.CA) > 0 {
		write("ca.crt", st.CA)
	}
	if err := g.loadDevice(); err != nil {
		return err
	}
	g.log.Info("device registered", "name", leaf.Subject.CommonName)
	return nil
}

func doJSON(ctx context.Context, c *http.Client, method, url string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Panel", "1")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		json.Unmarshal(b, &e)
		if e.Message == "" {
			e.Message = string(b)
		}
		return fmt.Errorf("panel: %s: %s", resp.Status, e.Message)
	}
	return json.Unmarshal(b, out)
}
