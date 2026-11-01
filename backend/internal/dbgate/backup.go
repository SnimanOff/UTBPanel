package dbgate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Keep is how many good backups are kept per bot. Damaged ones do not
// count, so good copies are never pushed out by bad ones.
var Keep = 7

// Backup is one copy of bot.db.
type Backup struct {
	Name          string    `cbor:"1,keyasint" json:"name"`
	Created       time.Time `cbor:"2,keyasint" json:"created"`
	Size          int64     `cbor:"3,keyasint" json:"size"`
	Reason        string    `cbor:"4,keyasint" json:"reason"` // daily | deploy | manual
	SchemaVersion int64     `cbor:"5,keyasint" json:"schema_version"`
	OK            bool      `cbor:"6,keyasint" json:"ok"`
	Error         string    `cbor:"7,keyasint" json:"error,omitempty"`
}

// Backups makes and restores copies under dir(botID).
type Backups struct {
	B   *Browser
	Dir func(botID int64) string
}

// SchemaVersion reads PRAGMA schema_version: SQLite bumps it on every
// CREATE, ALTER and DROP, so a schema change is seen without parsing SQL.
func (b *Browser) SchemaVersion(ctx context.Context, botID int64) (int64, error) {
	db, err := b.open(botID, false)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var v int64
	err = db.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&v)
	return v, err
}

// Integrity runs PRAGMA integrity_check (or quick_check) on a file opened
// read-only.
func Integrity(ctx context.Context, path string, quick bool) error {
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return err
	}
	defer db.Close()
	pragma := "integrity_check"
	if quick {
		pragma = "quick_check"
	}
	rows, err := db.QueryContext(ctx, `PRAGMA `+pragma)
	if err != nil {
		return err
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		if s != "ok" {
			problems = append(problems, s)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems[:min(len(problems), 5)], "; "))
	}
	return nil
}

// Make copies bot.db with VACUUM INTO, without stopping the bot, and checks
// the copy at once.
func (k *Backups) Make(ctx context.Context, botID int64, reason string) (Backup, error) {
	src := k.B.Path(botID)
	if _, err := os.Stat(src); err != nil {
		return Backup{}, ErrNoDB
	}
	dir := k.Dir(botID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Backup{}, err
	}
	now := time.Now().UTC()
	b := Backup{Name: now.Format("20060102-150405.000") + ".db", Created: now, Reason: reason}
	dst := filepath.Join(dir, b.Name)
	// The version of the live database at backup time: autorollback compares
	// it with the version after a deploy (VACUUM INTO resets the copy's own).
	b.SchemaVersion, _ = k.B.SchemaVersion(ctx, botID)
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return b, err
	}
	_, err = db.ExecContext(ctx, `VACUUM INTO ?`, dst)
	db.Close()
	if err != nil {
		return b, fmt.Errorf("dbgate: VACUUM INTO: %w", err)
	}
	if fi, err := os.Stat(dst); err == nil {
		b.Size = fi.Size()
	}
	if err := Integrity(ctx, dst, false); err != nil {
		b.Error = err.Error()
	} else {
		b.OK = true
	}
	if err := k.writeMeta(botID, b); err != nil {
		return b, err
	}
	k.prune(botID)
	if !b.OK {
		return b, fmt.Errorf("dbgate: backup failed the integrity check: %s", b.Error)
	}
	return b, nil
}

func (k *Backups) writeMeta(botID int64, b Backup) error {
	data, _ := json.Marshal(b)
	return os.WriteFile(filepath.Join(k.Dir(botID), b.Name+".json"), data, 0o600)
}

// List returns backups, newest first.
func (k *Backups) List(botID int64) ([]Backup, error) {
	ents, err := os.ReadDir(k.Dir(botID))
	if errors.Is(err, os.ErrNotExist) {
		return []Backup{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Backup{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".db.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(k.Dir(botID), e.Name()))
		if err != nil {
			continue
		}
		var b Backup
		if json.Unmarshal(data, &b) == nil {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// prune keeps the newest Keep good backups; damaged ones older than the
// oldest kept good one are removed too.
func (k *Backups) prune(botID int64) {
	bs, _ := k.List(botID)
	good := 0
	var oldestKept time.Time
	for _, b := range bs {
		if b.OK {
			good++
			if good <= Keep {
				oldestKept = b.Created
				continue
			}
		} else if oldestKept.IsZero() || b.Created.After(oldestKept) {
			continue
		}
		p := filepath.Join(k.Dir(botID), b.Name)
		os.Remove(p)
		os.Remove(p + ".json")
	}
}

// Path returns a backup file after checking the name.
func (k *Backups) Path(botID int64, name string) (string, error) {
	if name != filepath.Base(name) || !strings.HasSuffix(name, ".db") {
		return "", errors.New("dbgate: bad backup name")
	}
	p := filepath.Join(k.Dir(botID), name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("dbgate: no backup %s", name)
	}
	return p, nil
}

// Restore replaces bot.db with a backup. The caller stops the bot before
// and starts it after.
func (k *Backups) Restore(botID int64, name string, chown func(string) error) error {
	src, err := k.Path(botID, name)
	if err != nil {
		return err
	}
	dst := k.B.Path(botID)
	tmp := dst + ".restore"
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	out.Sync()
	out.Close()
	os.Remove(dst + "-wal")
	os.Remove(dst + "-shm")
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	if chown != nil {
		return chown(dst)
	}
	return nil
}
