package dbgate

import (
	"context"
	"encoding/json"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// CheckEvery: a damaged disk shows before a backup is needed.
var CheckEvery = 7 * 24 * time.Hour

// Check is the result of PRAGMA quick_check of one database.
type Check struct {
	Name  string    `cbor:"1,keyasint" json:"name"` // core.db, panel.db, bot 3 …
	BotID int64     `cbor:"2,keyasint" json:"bot_id,omitempty"`
	At    time.Time `cbor:"3,keyasint" json:"at"`
	OK    bool      `cbor:"4,keyasint" json:"ok"`
	Error string    `cbor:"5,keyasint" json:"error,omitempty"`
}

// CheckFile runs quick_check on a database file.
func CheckFile(ctx context.Context, name, path string, botID int64) Check {
	c := Check{Name: name, BotID: botID, At: time.Now().UTC(), OK: true}
	if err := Integrity(ctx, path, true); err != nil {
		c.OK, c.Error = false, err.Error()
	}
	return c
}

// Checks keeps the last results in a key-value table (kv in core.db,
// settings in panel.db) and runs again when a week has passed.
type Checks struct {
	DB    *store.DB
	Table string // kv | settings
	Run   func(ctx context.Context) []Check
	// OnBad is called for every failed check.
	OnBad func(Check)
}

const checksKey = "db_checks"

// Last returns the stored results.
func (c *Checks) Last(ctx context.Context) []Check {
	var raw string
	var out []Check
	if c.DB.R.QueryRowContext(ctx, `SELECT value FROM `+c.Table+` WHERE key = ?`, checksKey).Scan(&raw) == nil {
		json.Unmarshal([]byte(raw), &out)
	}
	if out == nil {
		out = []Check{}
	}
	return out
}

// RunNow checks everything and stores the results.
func (c *Checks) RunNow(ctx context.Context) []Check {
	res := c.Run(ctx)
	b, _ := json.Marshal(res)
	c.DB.W.ExecContext(ctx, `INSERT INTO `+c.Table+` (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, checksKey, string(b))
	for _, r := range res {
		if !r.OK && c.OnBad != nil {
			c.OnBad(r)
		}
	}
	return res
}

// Loop checks once a week until ctx ends.
func (c *Checks) Loop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		last := c.Last(ctx)
		if len(last) == 0 || time.Since(last[0].At) >= CheckEvery {
			c.RunNow(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
