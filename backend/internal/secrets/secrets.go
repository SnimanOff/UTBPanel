// Package secrets keeps bot tokens and other secrets.
//
// Secrets lie in core.db encrypted with AES-256-GCM. The master key lives
// in its own volume /keys, outside /data, and only the core has it, so a
// backup of /data alone is useless. Secrets are write-only: there is no
// operation to read one over the socket, the API or MCP. The core decrypts
// them in memory only to start a bot or call the Bot API.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// KeySize is the master key length.
const KeySize = 32

// Kinds of secrets.
const (
	KindValue = "value"
	KindFile  = "file"
)

// TokenName is the name under which the bot token is stored.
const TokenName = "BOT_TOKEN"

// WebhookSecretName is the secret the core sends with every update.
const WebhookSecretName = "WEBHOOK_SECRET"

var (
	ErrNoKeysDir = errors.New("secrets: the keys volume is missing; mount /keys (it holds the master key and must not be inside /data)")
	ErrNotFound  = errors.New("secrets: not found")
	ErrHasBots   = errors.New("secrets: a master key can be imported only while there are no bots")
)

// MasterKeyPath returns the master key file in dir.
func MasterKeyPath(dir string) string { return filepath.Join(dir, "master.key") }

// LoadMasterKey reads the master key from dir, creating it on first start.
// The directory itself must exist: a missing volume must not silently
// produce a key inside the container.
func LoadMasterKey(dir string) ([]byte, error) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return nil, ErrNoKeysDir
	}
	path := MasterKeyPath(dir)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, KeySize)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		return key, WriteMasterKey(dir, key)
	}
	if err != nil {
		return nil, err
	}
	return ParseMasterKey(b)
}

// ParseMasterKey accepts the hex text written by EncodeMasterKey.
func ParseMasterKey(b []byte) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != KeySize {
		return nil, errors.New("secrets: master key must be 64 hex characters")
	}
	return key, nil
}

// EncodeMasterKey formats a key for export.
func EncodeMasterKey(key []byte) []byte { return []byte(hex.EncodeToString(key) + "\n") }

// WriteMasterKey stores the key atomically with mode 0600.
func WriteMasterKey(dir string, key []byte) error {
	path := MasterKeyPath(dir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, EncodeMasterKey(key), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Box encrypts with the master key.
type Box struct {
	mu   sync.RWMutex
	aead cipher.AEAD
}

// NewBox makes a box for key.
func NewBox(key []byte) (*Box, error) {
	b := &Box{}
	return b, b.SetKey(key)
}

// SetKey replaces the master key (import).
func (b *Box) SetKey(key []byte) error {
	if len(key) != KeySize {
		return errors.New("secrets: master key must be 32 bytes")
	}
	c, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(c)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.aead = aead
	b.mu.Unlock()
	return nil
}

// aad binds a ciphertext to its bot and name, so it cannot be moved to
// another secret.
func aad(botID int64, name string) []byte {
	return []byte(strconv.FormatInt(botID, 10) + "\x00" + name)
}

// Seal encrypts plaintext for (botID, name).
func (b *Box) Seal(botID int64, name string, plaintext []byte) (ct, nonce []byte, err error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	nonce = make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return b.aead.Seal(nil, nonce, plaintext, aad(botID, name)), nonce, nil
}

// Open decrypts a value sealed for (botID, name).
func (b *Box) Open(botID int64, name string, ct, nonce []byte) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	pt, err := b.aead.Open(nil, nonce, ct, aad(botID, name))
	if err != nil {
		return nil, fmt.Errorf("secrets: %s of bot %d cannot be decrypted (wrong master key?)", name, botID)
	}
	return pt, nil
}

// Info describes a secret without its value.
type Info struct {
	Name      string    `cbor:"1,keyasint" json:"name"`
	Kind      string    `cbor:"2,keyasint" json:"kind"`
	UpdatedAt time.Time `cbor:"3,keyasint" json:"updated_at"`
}

// Store keeps secrets in core.db.
type Store struct {
	DB  *store.DB
	Box *Box
	// Redact receives every decrypted value so logs never show it.
	Redact *Redactor
}

// ValidName reports whether name can be an environment variable.
func ValidName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// Set stores or replaces a secret.
func (s *Store) Set(ctx context.Context, botID int64, name, kind string, value []byte) error {
	if !ValidName(name) {
		return fmt.Errorf("secrets: bad name %q: use letters, digits and _", name)
	}
	if kind != KindValue && kind != KindFile {
		return fmt.Errorf("secrets: bad kind %q", kind)
	}
	ct, nonce, err := s.Box.Seal(botID, name, value)
	if err != nil {
		return err
	}
	_, err = s.DB.W.ExecContext(ctx, `INSERT INTO secrets (bot_id, name, kind, ciphertext, nonce, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (bot_id, name) DO UPDATE SET kind = excluded.kind, ciphertext = excluded.ciphertext, nonce = excluded.nonce, updated_at = excluded.updated_at`,
		botID, name, kind, ct, nonce, store.Now())
	return err
}

// SetTx is Set inside a transaction.
func (s *Store) SetTx(ctx context.Context, tx *sql.Tx, botID int64, name, kind string, value []byte) error {
	ct, nonce, err := s.Box.Seal(botID, name, value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO secrets (bot_id, name, kind, ciphertext, nonce, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (bot_id, name) DO UPDATE SET kind = excluded.kind, ciphertext = excluded.ciphertext, nonce = excluded.nonce, updated_at = excluded.updated_at`,
		botID, name, kind, ct, nonce, store.Now())
	return err
}

// Delete removes a secret.
func (s *Store) Delete(ctx context.Context, botID int64, name string) error {
	res, err := s.DB.W.ExecContext(ctx, `DELETE FROM secrets WHERE bot_id = ? AND name = ?`, botID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns names, kinds and dates of a bot's secrets.
func (s *Store) List(ctx context.Context, botID int64) ([]Info, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT name, kind, updated_at FROM secrets WHERE bot_id = ? ORDER BY name`, botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Info
	for rows.Next() {
		var i Info
		var ts string
		if err := rows.Scan(&i.Name, &i.Kind, &ts); err != nil {
			return nil, err
		}
		i.UpdatedAt, _ = store.ParseTime(ts)
		out = append(out, i)
	}
	return out, rows.Err()
}

// Secret is a decrypted secret. It is only ever used inside the core.
type Secret struct {
	Name  string
	Kind  string
	Value []byte
}

// Decrypt returns one decrypted secret.
func (s *Store) Decrypt(ctx context.Context, botID int64, name string) ([]byte, error) {
	var ct, nonce []byte
	err := s.DB.R.QueryRowContext(ctx, `SELECT ciphertext, nonce FROM secrets WHERE bot_id = ? AND name = ?`, botID, name).Scan(&ct, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v, err := s.Box.Open(botID, name, ct, nonce)
	if err == nil && s.Redact != nil {
		s.Redact.Add(string(v))
	}
	return v, err
}

// DecryptAll returns all secrets of a bot, for starting it.
func (s *Store) DecryptAll(ctx context.Context, botID int64) ([]Secret, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT name, kind, ciphertext, nonce FROM secrets WHERE bot_id = ? ORDER BY name`, botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Secret
	for rows.Next() {
		var sec Secret
		var ct, nonce []byte
		if err := rows.Scan(&sec.Name, &sec.Kind, &ct, &nonce); err != nil {
			return nil, err
		}
		if sec.Value, err = s.Box.Open(botID, sec.Name, ct, nonce); err != nil {
			return nil, err
		}
		if s.Redact != nil {
			s.Redact.Add(string(sec.Value))
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

// WriteFiles puts file secrets into dir with mode 0400 owned by uid (when
// uid >= 0) and returns name → path.
func WriteFiles(dir string, uid int, secs []Secret) (map[string]string, error) {
	paths := map[string]string{}
	for _, s := range secs {
		if s.Kind != KindFile {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		p := filepath.Join(dir, s.Name)
		os.Remove(p) // it is 0400: replace instead of overwriting
		if err := os.WriteFile(p, s.Value, 0o400); err != nil {
			return nil, err
		}
		if uid >= 0 {
			if err := os.Chown(p, uid, uid); err != nil {
				return nil, err
			}
		}
		paths[s.Name] = p
	}
	if uid >= 0 && len(paths) > 0 {
		os.Chown(dir, uid, uid)
	}
	return paths, nil
}
