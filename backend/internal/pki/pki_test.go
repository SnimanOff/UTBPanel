package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func newCA(t *testing.T) (*CA, *store.DB, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ca, err := Open(ctx, filepath.Join(dir, "pki"), db)
	if err != nil {
		t.Fatal(err)
	}
	return ca, db, dir
}

func key(t *testing.T) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestIssueVerifyRevoke(t *testing.T) {
	ctx := context.Background()
	ca, _, _ := newCA(t)
	cert, err := ca.Issue(ctx, Owner, "home-pc", "", &key(t).PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ca.Verify(cert)
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "home-pc" || id.Kind != Owner || id.Serial != SerialString(cert.SerialNumber) {
		t.Fatalf("identity = %+v", id)
	}
	if err := ca.Revoke(ctx, id.Serial); err != nil {
		t.Fatal(err)
	}
	if _, err := ca.Verify(cert); !errors.Is(err, ErrRevoked) {
		t.Fatalf("after revoke: %v", err)
	}
}

func TestReopenKeepsCA(t *testing.T) {
	ctx := context.Background()
	ca, db, dir := newCA(t)
	cert, _ := ca.Issue(ctx, Child, "laptop", "", &key(t).PublicKey)
	ca2, err := Open(ctx, filepath.Join(dir, "pki"), db)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca2.Fingerprint()) != string(ca.Fingerprint()) {
		t.Fatal("fingerprint changed after reopen")
	}
	if _, err := ca2.Verify(cert); err != nil {
		t.Fatal(err)
	}
}

func TestForeignCertificate(t *testing.T) {
	ctx := context.Background()
	ca, _, _ := newCA(t)
	other, _, _ := newCA(t)
	cert, _ := other.Issue(ctx, Owner, "x", "", &key(t).PublicKey)
	if _, err := ca.Verify(cert); err == nil {
		t.Fatal("certificate of another CA accepted")
	}
}

func TestUnregisteredSerial(t *testing.T) {
	ctx := context.Background()
	ca, db, _ := newCA(t)
	cert, _ := ca.Issue(ctx, Owner, "x", "", &key(t).PublicKey)
	db.W.Exec(`DELETE FROM keys`)
	ca.Reload(ctx)
	if _, err := ca.Verify(cert); !errors.Is(err, ErrUnknown) {
		t.Fatalf("got %v", err)
	}
}

func TestExpiredEnrollment(t *testing.T) {
	ctx := context.Background()
	ca, db, _ := newCA(t)
	cert, err := ca.Issue(ctx, Enrollment, "new", "", &key(t).PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if d := cert.NotAfter.Sub(cert.NotBefore); d > EnrollmentValidity+10*time.Minute {
		t.Fatalf("enrollment validity = %v", d)
	}
	if KindOf(cert) != Enrollment {
		t.Fatal("kind extension missing")
	}
	// Re-sign the same certificate with a past validity window.
	tpl := *cert
	tpl.NotBefore = time.Now().Add(-48 * time.Hour)
	tpl.NotAfter = time.Now().Add(-24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, &tpl, ca.cert, cert.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := x509.ParseCertificate(der)
	if _, err := ca.Verify(old); !errors.Is(err, ErrExpired) {
		t.Fatalf("got %v", err)
	}
	// Single use.
	serial := SerialString(cert.SerialNumber)
	err = db.Tx(ctx, func(tx *sql.Tx) error { return ca.MarkUsed(ctx, tx, serial) })
	if err != nil {
		t.Fatal(err)
	}
	err = db.Tx(ctx, func(tx *sql.Tx) error { return ca.MarkUsed(ctx, tx, serial) })
	if !errors.Is(err, ErrUsed) {
		t.Fatalf("second use: %v", err)
	}
	ca.Reload(ctx)
	if id, err := ca.Verify(cert); err != nil || !id.Used {
		t.Fatalf("used enrollment: %+v, %v", id, err)
	}
}

func TestServerCert(t *testing.T) {
	ca, _, _ := newCA(t)
	c1, _, err := ca.ServerCert("203.0.113.10:443")
	if err != nil {
		t.Fatal(err)
	}
	if err := c1.VerifyHostname("203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	c2, _, _ := ca.ServerCert("203.0.113.10")
	if c1.SerialNumber.Cmp(c2.SerialNumber) != 0 {
		t.Fatal("server certificate reissued without need")
	}
	c3, _, _ := ca.ServerCert("panel.example.com")
	if c3.VerifyHostname("panel.example.com") != nil {
		t.Fatal("new address not in SAN")
	}
}
