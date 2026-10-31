// Package pki is the panel's own certificate authority (plan 2).
//
// The CA signs the server certificate for :443 and client certificates of
// three kinds, told apart by a private extension: owner and child device
// keys, and one-off enrollment certificates that are valid for 24 hours and
// only for POST /enroll. Every serial number is recorded in panel.db, so
// revocation takes effect on the next TLS handshake.
package pki

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// Kind is the kind of a client certificate.
type Kind string

// Certificate kinds.
const (
	Owner      Kind = "owner"
	Child      Kind = "child"
	Enrollment Kind = "enrollment"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { return k == Owner || k == Child || k == Enrollment }

// OIDKind is the private extension holding the certificate kind
// (a UUID-based OID, 2.25.<uuid>, needs no registration).
var OIDKind = asn1.ObjectIdentifier{2, 25, 217308347, 118062435, 417592357, 214591820, 611}

const (
	caValidity         = 10 * 365 * 24 * time.Hour
	deviceValidity     = 10 * 365 * 24 * time.Hour
	EnrollmentValidity = 24 * time.Hour
	serverValidity     = 2 * 365 * 24 * time.Hour
)

var (
	ErrUnknown = errors.New("pki: unknown certificate")
	ErrRevoked = errors.New("pki: certificate revoked")
	ErrExpired = errors.New("pki: certificate expired")
	ErrUsed    = errors.New("pki: enrollment certificate already used")
)

// Identity is who presented a client certificate.
type Identity struct {
	Serial string
	Name   string
	Kind   Kind
	// Used is set for a spent enrollment certificate: it may still fetch
	// the result of its request, nothing else.
	Used bool
}

// CA holds the authority's key and the serial registry.
type CA struct {
	dir  string
	db   *store.DB
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pool *x509.CertPool

	mu      sync.RWMutex
	revoked map[string]serialState
}

type serialState uint8

const (
	serialActive serialState = iota
	serialRevoked
	serialUsed
)

// Open loads the CA from dir (creating it on first start) and the revoked
// serials from db.
func Open(ctx context.Context, dir string, db *store.DB) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ca := &CA{dir: dir, db: db}
	keyPath, certPath := filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.crt")
	if _, err := os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
		if err := ca.create(keyPath, certPath); err != nil {
			return nil, err
		}
	} else {
		key, err := readKey(keyPath)
		if err != nil {
			return nil, err
		}
		cert, err := readCert(certPath)
		if err != nil {
			return nil, err
		}
		ca.key, ca.cert = key, cert
	}
	ca.pool = x509.NewCertPool()
	ca.pool.AddCert(ca.cert)
	if err := ca.loadRevoked(ctx); err != nil {
		return nil, err
	}
	return ca, nil
}

func (ca *CA) create(keyPath, certPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tpl := &x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: "TG Panel CA", Organization: []string{"tgpanel"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	cert, _ := x509.ParseCertificate(der)
	if err := writeKey(keyPath, key); err != nil {
		return err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	ca.key, ca.cert = key, cert
	return nil
}

// Cert returns the CA certificate.
func (ca *CA) Cert() *x509.Certificate { return ca.cert }

// Pool returns a pool holding only the CA.
func (ca *CA) Pool() *x509.CertPool { return ca.pool }

// Fingerprint returns SHA-256 of the CA public key (SPKI); gate pins it.
func (ca *CA) Fingerprint() []byte { return Fingerprint(ca.cert) }

// Fingerprint returns SHA-256 of a certificate's SPKI.
func Fingerprint(c *x509.Certificate) []byte {
	h := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return h[:]
}

func newSerial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n.Add(n, big.NewInt(1))
}

// SerialString formats a serial the way it is stored.
func SerialString(n *big.Int) string { return hex.EncodeToString(n.Bytes()) }

// ServerCert returns the :443 certificate, reissuing it when missing, close
// to expiry or issued for another address.
func (ca *CA) ServerCert(addr string) (*x509.Certificate, crypto.Signer, error) {
	keyPath, certPath := filepath.Join(ca.dir, "server.key"), filepath.Join(ca.dir, "server.crt")
	if key, err := readKey(keyPath); err == nil {
		if cert, err := readCert(certPath); err == nil && time.Until(cert.NotAfter) > 30*24*time.Hour && cert.VerifyHostname(hostOf(addr)) == nil {
			return cert, key, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: "TG Panel"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(serverValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	if h := hostOf(addr); h != "" {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	if err := writeKey(keyPath, key); err != nil {
		return nil, nil, err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return nil, nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key, nil
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// Issue signs a client certificate for pub and records its serial. For
// enrollment certificates validity is fixed at 24 hours.
func (ca *CA) Issue(ctx context.Context, kind Kind, name, device string, pub crypto.PublicKey) (*x509.Certificate, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("pki: bad kind %q", kind)
	}
	validity := deviceValidity
	if kind == Enrollment {
		validity = EnrollmentValidity
	}
	kindExt, _ := asn1.Marshal(string(kind))
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:    newSerial(),
		Subject:         pkix.Name{CommonName: name, OrganizationalUnit: []string{string(kind)}},
		NotBefore:       now.Add(-5 * time.Minute),
		NotAfter:        now.Add(validity),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{{Id: OIDKind, Value: kindExt}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, pub, ca.key)
	if err != nil {
		return nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	_, err = ca.db.W.ExecContext(ctx, `INSERT INTO keys (serial, name, kind, device, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		SerialString(cert.SerialNumber), name, string(kind), device, store.FormatTime(now), store.FormatTime(cert.NotAfter))
	if err != nil {
		return nil, err
	}
	ca.mu.Lock()
	ca.revoked[SerialString(cert.SerialNumber)] = serialActive
	ca.mu.Unlock()
	return cert, nil
}

// KindOf reads the kind extension.
func KindOf(c *x509.Certificate) Kind {
	for _, e := range c.Extensions {
		if e.Id.Equal(OIDKind) {
			var s string
			if _, err := asn1.Unmarshal(e.Value, &s); err == nil {
				return Kind(s)
			}
		}
	}
	return ""
}

// Verify checks a client certificate chain against the CA and the
// registry. It is cheap enough for every TLS handshake.
func (ca *CA) Verify(c *x509.Certificate) (Identity, error) {
	if _, err := c.Verify(x509.VerifyOptions{Roots: ca.pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		var inv x509.CertificateInvalidError
		if errors.As(err, &inv) && inv.Reason == x509.Expired {
			return Identity{}, ErrExpired
		}
		return Identity{}, err
	}
	id := Identity{Serial: SerialString(c.SerialNumber), Name: c.Subject.CommonName, Kind: KindOf(c)}
	if !id.Kind.Valid() {
		return Identity{}, ErrUnknown
	}
	ca.mu.RLock()
	st, known := ca.revoked[id.Serial]
	ca.mu.RUnlock()
	switch {
	case !known:
		return Identity{}, ErrUnknown
	case st == serialRevoked:
		return Identity{}, ErrRevoked
	case st == serialUsed:
		id.Used = true
	}
	return id, nil
}

// loadRevoked reads the registry into memory: serial → revoked.
func (ca *CA) loadRevoked(ctx context.Context) error {
	rows, err := ca.db.R.QueryContext(ctx, `SELECT serial, revoked_at IS NOT NULL, used_at IS NOT NULL FROM keys`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[string]serialState{}
	for rows.Next() {
		var s string
		var revoked, used bool
		if err := rows.Scan(&s, &revoked, &used); err != nil {
			return err
		}
		switch {
		case revoked:
			m[s] = serialRevoked
		case used:
			m[s] = serialUsed
		default:
			m[s] = serialActive
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	ca.mu.Lock()
	ca.revoked = m
	ca.mu.Unlock()
	return nil
}

// Reload refreshes the in-memory registry after changes in panel.db.
func (ca *CA) Reload(ctx context.Context) error { return ca.loadRevoked(ctx) }

// Revoke marks a serial as revoked; it stops working on the next handshake.
func (ca *CA) Revoke(ctx context.Context, serial string) error {
	res, err := ca.db.W.ExecContext(ctx, `UPDATE keys SET revoked_at = ? WHERE serial = ? AND revoked_at IS NULL`, store.Now(), serial)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUnknown
	}
	return ca.Reload(ctx)
}

// MarkUsed retires an enrollment certificate inside tx.
func (ca *CA) MarkUsed(ctx context.Context, tx *sql.Tx, serial string) error {
	res, err := tx.ExecContext(ctx, `UPDATE keys SET used_at = ? WHERE serial = ? AND kind = 'enrollment' AND used_at IS NULL`, store.Now(), serial)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUsed
	}
	return nil
}

func readKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("pki: %s is not PEM", path)
	}
	return x509.ParseECPrivateKey(blk.Bytes)
}

func readCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("pki: %s is not PEM", path)
	}
	return x509.ParseCertificate(blk.Bytes)
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, "EC PRIVATE KEY", der, 0o600)
}

func writePEM(path, typ string, der []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
