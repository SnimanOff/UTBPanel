package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/gatefile"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// Key is one row of the key registry.
type Key struct {
	Serial    string     `json:"serial"`
	Name      string     `json:"name"`
	Kind      Kind       `json:"kind"`
	Grants    Kind       `json:"grants,omitempty"` // enrollment: owner or child
	Device    string     `json:"device,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	LastIP    string     `json:"last_ip,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,39}$`)

// ErrBadName is returned for key names that cannot be used in file names.
var ErrBadName = errors.New("pki: key name must be 1-40 letters, digits, '.', '_' or '-'")

// ErrOwnerKey refuses to revoke the owner key from the panel (plan 2.3).
var ErrOwnerKey = errors.New("pki: the owner key can only be revoked over SSH: panel keys revoke")

// NewGate issues an enrollment certificate for a future device key of kind
// grants and returns the data block for gate.exe. The certificate's private
// key is a software key: it is single-use, lives 24 hours and is useless
// without the owner confirming the code.
func (ca *CA) NewGate(ctx context.Context, grants Kind, name string, servers []string) (*gatefile.Block, error) {
	if grants != Owner && grants != Child {
		return nil, fmt.Errorf("pki: a gate can grant only owner or child, not %q", grants)
	}
	if !nameRe.MatchString(name) {
		return nil, ErrBadName
	}
	var n int
	if err := ca.db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM keys WHERE name = ? AND kind != 'enrollment' AND revoked_at IS NULL`, name).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, fmt.Errorf("pki: key %q already exists", name)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	cert, err := ca.Issue(ctx, Enrollment, name, "", &key.PublicKey)
	if err != nil {
		return nil, err
	}
	if _, err := ca.db.W.ExecContext(ctx, `UPDATE keys SET grants = ? WHERE serial = ?`, string(grants), SerialString(cert.SerialNumber)); err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &gatefile.Block{
		Servers:       servers,
		CAFingerprint: ca.Fingerprint(),
		EnrollCert:    cert.Raw,
		EnrollKey:     der,
		KeyName:       name,
		Kind:          string(grants),
	}, nil
}

// WriteGate streams gate.exe from exePath with the block appended.
func WriteGate(w io.Writer, exePath string, b *gatefile.Block) error {
	f, err := os.Open(exePath)
	if err != nil {
		return fmt.Errorf("gate.exe: %w", err)
	}
	defer f.Close()
	_, err = gatefile.Write(w, f, b)
	return err
}

func parseTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := store.ParseTime(ns.String)
	if err != nil {
		return nil
	}
	return &t
}

// List returns all keys, newest first.
func (ca *CA) List(ctx context.Context) ([]Key, error) {
	rows, err := ca.db.R.QueryContext(ctx, `SELECT serial, name, kind, grants, device, created_at, expires_at, last_seen, last_ip, revoked_at, used_at
		FROM keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		var created string
		var expires, seen, revoked, used sql.NullString
		if err := rows.Scan(&k.Serial, &k.Name, &k.Kind, &k.Grants, &k.Device, &created, &expires, &seen, &k.LastIP, &revoked, &used); err != nil {
			return nil, err
		}
		k.CreatedAt, _ = store.ParseTime(created)
		k.ExpiresAt, k.LastSeen, k.RevokedAt, k.UsedAt = parseTime(expires), parseTime(seen), parseTime(revoked), parseTime(used)
		out = append(out, k)
	}
	return out, rows.Err()
}

// Find returns the active key with this serial or name.
func (ca *CA) Find(ctx context.Context, serialOrName string) (Key, error) {
	keys, err := ca.List(ctx)
	if err != nil {
		return Key{}, err
	}
	var found []Key
	for _, k := range keys {
		if k.RevokedAt != nil {
			continue
		}
		if k.Serial == serialOrName || (k.Name == serialOrName && k.Kind != Enrollment) {
			found = append(found, k)
		}
	}
	switch len(found) {
	case 0:
		return Key{}, ErrUnknown
	case 1:
		return found[0], nil
	}
	return Key{}, fmt.Errorf("pki: several keys match %q, use the serial", serialOrName)
}

// RevokeKey revokes a key by serial or name. The owner key is refused
// unless allowOwner (CLI over SSH).
func (ca *CA) RevokeKey(ctx context.Context, serialOrName string, allowOwner bool) (Key, error) {
	k, err := ca.Find(ctx, serialOrName)
	if err != nil {
		return Key{}, err
	}
	if k.Kind == Owner && !allowOwner {
		return Key{}, ErrOwnerKey
	}
	return k, ca.Revoke(ctx, k.Serial)
}

// FormatList renders keys for the CLI.
func FormatList(keys []Key) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-20s %-10s %-8s %-20s %-15s %s\n", "NAME", "KIND", "STATE", "LAST SEEN", "LAST IP", "SERIAL")
	for _, k := range keys {
		state := "active"
		switch {
		case k.RevokedAt != nil:
			state = "revoked"
		case k.UsedAt != nil:
			state = "used"
		case k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt):
			state = "expired"
		case k.Kind == Enrollment:
			state = "waiting"
		}
		kind := string(k.Kind)
		if k.Kind == Enrollment {
			kind = "enroll:" + string(k.Grants)
		}
		seen := "-"
		if k.LastSeen != nil {
			seen = k.LastSeen.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "%-20s %-10s %-8s %-20s %-15s %s\n", k.Name, kind, state, seen, k.LastIP, k.Serial)
	}
	return b.String()
}
