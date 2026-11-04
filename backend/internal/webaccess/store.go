package webaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// Roles of an access.
const (
	RoleView   = "view"
	RoleManage = "manage"
	RoleFull   = "full" // only with all bots: what a child key can do
)

// Lifetimes.
var (
	InviteTTL  = 24 * time.Hour
	SessionTTL = 12 * time.Hour
	PendingTTL = 24 * time.Hour
)

// Access is a person with passkeys.
type Access struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	Role        string       `json:"role"`
	AllBots     bool         `json:"all_bots"`
	Bots        []int64      `json:"bots"`
	CreatedAt   time.Time    `json:"created_at"`
	CreatedBy   string       `json:"created_by"`
	Credentials []CredInfo   `json:"credentials"`
	Invite      *InviteState `json:"invite,omitempty"`
}

// CredInfo is a passkey as the owner sees it.
type CredInfo struct {
	ID        string     `json:"id"` // base64url
	Code      string     `json:"code"`
	Approved  bool       `json:"approved"`
	Device    string     `json:"device"`
	IP        string     `json:"ip"`
	CreatedAt time.Time  `json:"created_at"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
}

// InviteState tells whether a link is still waiting.
type InviteState struct {
	ExpiresAt time.Time `json:"expires_at"`
}

// Errors.
var (
	ErrNotFound = errors.New("webaccess: not found")
	ErrExpired  = errors.New("webaccess: the link is expired or used")
)

// Store keeps accesses in panel.db.
type Store struct{ DB *store.DB }

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// Create adds an access and returns it with a fresh invite token.
func (s *Store) Create(ctx context.Context, a Access) (Access, string, error) {
	if a.Bots == nil {
		a.Bots = []int64{}
	}
	bots, _ := json.Marshal(a.Bots)
	now := time.Now()
	var token string
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO web_access (name, role, all_bots, bots, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
			a.Name, a.Role, a.AllBots, string(bots), now.Unix(), a.CreatedBy)
		if err != nil {
			return err
		}
		a.ID, _ = res.LastInsertId()
		token, err = s.inviteTx(ctx, tx, a.ID)
		return err
	})
	a.CreatedAt = now.UTC()
	return a, token, err
}

func (s *Store) inviteTx(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	token := b64.EncodeToString(random(24))
	_, err := tx.ExecContext(ctx, `INSERT INTO web_invites (token_hash, access_id, expires_at) VALUES (?, ?, ?)`,
		hash(token), id, time.Now().Add(InviteTTL).Unix())
	return token, err
}

// Invite issues a new link for an existing access (another phone).
func (s *Store) Invite(ctx context.Context, id int64) (string, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return "", err
	}
	var token string
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		token, err = s.inviteTx(ctx, tx, id)
		return err
	})
	return token, err
}

func scanAccess(row interface{ Scan(...any) error }) (Access, error) {
	var a Access
	var bots string
	var created int64
	if err := row.Scan(&a.ID, &a.Name, &a.Role, &a.AllBots, &bots, &created, &a.CreatedBy); err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(bots), &a.Bots); err != nil {
		return a, err
	}
	if a.Bots == nil {
		a.Bots = []int64{}
	}
	a.CreatedAt = time.Unix(created, 0).UTC()
	return a, nil
}

const accessCols = `id, name, role, all_bots, bots, created_at, created_by`

// Get returns one access without its credentials.
func (s *Store) Get(ctx context.Context, id int64) (Access, error) {
	a, err := scanAccess(s.DB.R.QueryRowContext(ctx, `SELECT `+accessCols+` FROM web_access WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// List returns every access with its passkeys and open invites.
func (s *Store) List(ctx context.Context) ([]Access, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT `+accessCols+` FROM web_access ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []Access
	for rows.Next() {
		a, err := scanAccess(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		a.Credentials = []CredInfo{}
		out = append(out, a)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		crows, err := s.DB.R.QueryContext(ctx, `SELECT id, code, approved, device, ip, created_at, last_used FROM web_credentials WHERE access_id = ? ORDER BY created_at`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for crows.Next() {
			var c CredInfo
			var id []byte
			var created int64
			var used sql.NullInt64
			if err := crows.Scan(&id, &c.Code, &c.Approved, &c.Device, &c.IP, &created, &used); err != nil {
				_ = crows.Close()
				return nil, err
			}
			c.ID = b64.EncodeToString(id)
			c.CreatedAt = time.Unix(created, 0).UTC()
			if used.Valid {
				t := time.Unix(used.Int64, 0).UTC()
				c.LastUsed = &t
			}
			out[i].Credentials = append(out[i].Credentials, c)
		}
		_ = crows.Close()
		var exp int64
		if s.DB.R.QueryRowContext(ctx, `SELECT MAX(expires_at) FROM web_invites WHERE access_id = ? AND used_at IS NULL AND expires_at > ?`,
			out[i].ID, time.Now().Unix()).Scan(&exp) == nil && exp > 0 {
			out[i].Invite = &InviteState{ExpiresAt: time.Unix(exp, 0).UTC()}
		}
	}
	if out == nil {
		out = []Access{}
	}
	return out, nil
}

// Delete removes an access with its passkeys, invites and sessions.
func (s *Store) Delete(ctx context.Context, id int64) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM web_sessions WHERE access_id = ?`,
			`DELETE FROM web_credentials WHERE access_id = ?`,
			`DELETE FROM web_invites WHERE access_id = ?`,
			`DELETE FROM web_access WHERE id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// InviteAccess returns the access an unused, unexpired invite leads to.
func (s *Store) InviteAccess(ctx context.Context, token string) (Access, error) {
	var id int64
	err := s.DB.R.QueryRowContext(ctx, `SELECT access_id FROM web_invites WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		hash(token), time.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Access{}, ErrExpired
	}
	if err != nil {
		return Access{}, err
	}
	return s.Get(ctx, id)
}

// Enroll spends the invite and stores a passkey waiting for approval. It
// returns the poll token of the waiting page.
func (s *Store) Enroll(ctx context.Context, token string, accessID int64, c Credential, code, device, ip string) (string, error) {
	poll := b64.EncodeToString(random(24))
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE web_invites SET used_at = ? WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
			time.Now().Unix(), hash(token), time.Now().Unix())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrExpired
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO web_credentials (id, access_id, public_key, alg, sign_count, code, poll_hash, device, ip, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, accessID, c.PublicKey, c.Alg, c.Count, code, hash(poll), device, ip, time.Now().Unix())
		return err
	})
	return poll, err
}

// Approve marks a passkey approved.
func (s *Store) Approve(ctx context.Context, credID []byte) error {
	res, err := s.DB.W.ExecContext(ctx, `UPDATE web_credentials SET approved = 1 WHERE id = ?`, credID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveCredential deletes a passkey and its sessions.
func (s *Store) RemoveCredential(ctx context.Context, credID []byte) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE credential_id = ?`, credID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM web_credentials WHERE id = ?`, credID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// stored is a passkey for sign-in.
type stored struct {
	ID        []byte
	AccessID  int64
	PublicKey []byte
	Alg       int64
	Count     uint32
	Approved  bool
}

func (s *Store) credential(ctx context.Context, id []byte) (stored, error) {
	var c stored
	err := s.DB.R.QueryRowContext(ctx, `SELECT id, access_id, public_key, alg, sign_count, approved FROM web_credentials WHERE id = ?`, id).
		Scan(&c.ID, &c.AccessID, &c.PublicKey, &c.Alg, &c.Count, &c.Approved)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// Poll answers the waiting page: approved (with the credential id),
// waiting, or gone (rejected or expired).
func (s *Store) Poll(ctx context.Context, poll string) (state string, cred stored, err error) {
	err = s.DB.R.QueryRowContext(ctx, `SELECT id, access_id, approved, created_at FROM web_credentials WHERE poll_hash = ?`, hash(poll)).
		Scan(&cred.ID, &cred.AccessID, &cred.Approved, new(int64))
	if errors.Is(err, sql.ErrNoRows) {
		return "gone", cred, nil
	}
	if err != nil {
		return "", cred, err
	}
	if !cred.Approved {
		return "waiting", cred, nil
	}
	// The poll token gives one session, once.
	if _, err := s.DB.W.ExecContext(ctx, `UPDATE web_credentials SET poll_hash = NULL WHERE id = ?`, cred.ID); err != nil {
		return "", cred, err
	}
	return "approved", cred, nil
}

// Session creates a session for a passkey and returns its token.
func (s *Store) Session(ctx context.Context, credID []byte, accessID int64, ip string, count uint32) (string, error) {
	token := b64.EncodeToString(random(32))
	now := time.Now()
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO web_sessions (token_hash, credential_id, access_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			hash(token), credID, accessID, now.Unix(), now.Add(SessionTTL).Unix()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE web_credentials SET last_used = ?, ip = ?, sign_count = MAX(sign_count, ?) WHERE id = ?`, now.Unix(), ip, count, credID)
		return err
	})
	return token, err
}

// Lookup returns the access of a live session.
func (s *Store) Lookup(ctx context.Context, token string) (Access, []byte, error) {
	var id int64
	var cred []byte
	err := s.DB.R.QueryRowContext(ctx, `SELECT s.access_id, s.credential_id FROM web_sessions s
		JOIN web_credentials c ON c.id = s.credential_id AND c.approved = 1
		WHERE s.token_hash = ? AND s.expires_at > ?`, hash(token), time.Now().Unix()).Scan(&id, &cred)
	if errors.Is(err, sql.ErrNoRows) {
		return Access{}, nil, ErrNotFound
	}
	if err != nil {
		return Access{}, nil, err
	}
	a, err := s.Get(ctx, id)
	return a, cred, err
}

// EndSession deletes one session.
func (s *Store) EndSession(ctx context.Context, token string) {
	_, _ = s.DB.W.ExecContext(ctx, `DELETE FROM web_sessions WHERE token_hash = ?`, hash(token))
}

// Prune drops expired sessions, invites and passkeys never approved.
func (s *Store) Prune(ctx context.Context) {
	now := time.Now()
	_, _ = s.DB.W.ExecContext(ctx, `DELETE FROM web_sessions WHERE expires_at < ?`, now.Unix())
	_, _ = s.DB.W.ExecContext(ctx, `DELETE FROM web_invites WHERE expires_at < ?`, now.Add(-7*24*time.Hour).Unix())
	_, _ = s.DB.W.ExecContext(ctx, `DELETE FROM web_credentials WHERE approved = 0 AND created_at < ?`, now.Add(-PendingTTL).Unix())
}
