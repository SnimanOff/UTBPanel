package sdkhost

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

type Flag struct {
	Name        string      `cbor:"1,keyasint" json:"name"`
	Kind        string      `cbor:"2,keyasint" json:"kind"`
	Description string      `cbor:"3,keyasint" json:"description"`
	Default     flags.Value `cbor:"4,keyasint" json:"default"`
	Value       flags.Value `cbor:"5,keyasint" json:"value"`
	Overridden  bool        `cbor:"6,keyasint" json:"overridden"`
	Version     int64       `cbor:"7,keyasint" json:"version"`
}
type FlagMark = metrics.FlagMark
type Flags struct {
	DB      *store.DB
	Changed func(int64, string)
	mu      sync.Mutex
	peers   map[key]*wire.Conn
}

func parseFlag(kind, raw string) (flags.Value, error) {
	var v flags.Value
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, err
	}
	return flags.Normalize(kind, v)
}
func instanceColumn(instance string) string {
	if instance == "canary" {
		return "canary_active"
	}
	return "main_active"
}

func (f *Flags) Register(mux *wire.Mux, id int64, instance string) {
	mux.Handle("sdk.flags.register", wire.Func(func(ctx context.Context, c *wire.Conn, defs []flags.Definition) (map[string]flags.State, error) {
		return f.RegisterDefinitions(ctx, id, instance, c, defs)
	}))
}

func (f *Flags) RegisterDefinitions(ctx context.Context, id int64, instance string, c *wire.Conn, defs []flags.Definition) (map[string]flags.State, error) {
	if len(defs) > flags.MaxFlags {
		return nil, fmt.Errorf("sdk: flag limit reached")
	}
	wanted := map[string]bool{}
	for _, d := range defs {
		if err := flags.Validate(d); err != nil {
			return nil, err
		}
		if wanted[d.Name] {
			return nil, fmt.Errorf("sdk: duplicate flag")
		}
		wanted[d.Name] = true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]flags.State{}
	err := f.DB.Tx(ctx, func(tx *sql.Tx) error {
		column := instanceColumn(instance)
		other := "canary_active"
		if instance == "canary" {
			other = "main_active"
		}
		rows, err := tx.QueryContext(ctx, `SELECT name FROM sdk_flags WHERE bot_id=? AND `+other+`=1`, id)
		if err != nil {
			return err
		}
		all := map[string]bool{}
		for name := range wanted {
			all[name] = true
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			all[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(all) > flags.MaxFlags {
			return fmt.Errorf("sdk: flag limit reached")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sdk_flags SET `+column+`=0 WHERE bot_id=?`, id); err != nil {
			return err
		}
		for _, d := range defs {
			defaultJSON, err := json.Marshal(d.Default)
			if err != nil {
				return err
			}
			var kind, description, defaultRaw string
			var override sql.NullString
			var version int64
			var mainActive bool
			err = tx.QueryRowContext(ctx, `SELECT kind,description,default_json,override_json,version,main_active FROM sdk_flags WHERE bot_id=? AND name=?`, id, d.Name).Scan(&kind, &description, &defaultRaw, &override, &version, &mainActive)
			if err == sql.ErrNoRows {
				kind = d.Kind
				version = 1
				_, err = tx.ExecContext(ctx, `INSERT INTO sdk_flags(bot_id,name,kind,description,default_json,`+column+`) VALUES(?,?,?,?,?,1)`, id, d.Name, d.Kind, d.Description, string(defaultJSON))
			} else if err == nil {
				if instance != "canary" || !mainActive {
					if kind != d.Kind {
						override = sql.NullString{}
					}
					if kind != d.Kind || description != d.Description || defaultRaw != string(defaultJSON) {
						version++
					}
					kind = d.Kind
					var raw any
					if override.Valid {
						raw = override.String
					}
					_, err = tx.ExecContext(ctx, `UPDATE sdk_flags SET kind=?,description=?,default_json=?,override_json=?,version=?,`+column+`=1 WHERE bot_id=? AND name=?`, kind, d.Description, string(defaultJSON), raw, version, id, d.Name)
				} else {
					_, err = tx.ExecContext(ctx, `UPDATE sdk_flags SET `+column+`=1 WHERE bot_id=? AND name=?`, id, d.Name)
				}
			}
			if err != nil {
				return err
			}
			state := flags.State{Value: d.Default, Version: version, Reset: true}
			if override.Valid && kind == d.Kind {
				v, err := parseFlag(kind, override.String)
				if err != nil {
					return err
				}
				state.Value = v
				state.Reset = false
			}
			out[d.Name] = state
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sdk_flags WHERE bot_id=? AND main_active=0 AND canary_active=0`, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if c == nil {
		return out, nil
	}
	if f.peers == nil {
		f.peers = map[key]*wire.Conn{}
	}
	f.peers[key{id, instance}] = c
	return out, nil
}

func (f *Flags) Disconnect(id int64, instance string, c *wire.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.peers[key{id, instance}] != c {
		return
	}
	delete(f.peers, key{id, instance})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f.DB.W.ExecContext(ctx, `UPDATE sdk_flags SET `+instanceColumn(instance)+`=0 WHERE bot_id=?`, id)
}

func (f *Flags) List(ctx context.Context, id int64) ([]Flag, error) {
	rows, err := f.DB.R.QueryContext(ctx, `SELECT name,kind,description,default_json,override_json,version FROM sdk_flags WHERE bot_id=? ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Flag{}
	for rows.Next() {
		var v Flag
		var def string
		var override sql.NullString
		if err := rows.Scan(&v.Name, &v.Kind, &v.Description, &def, &override, &v.Version); err != nil {
			return nil, err
		}
		v.Default, err = parseFlag(v.Kind, def)
		if err != nil {
			return nil, err
		}
		v.Value = v.Default
		if override.Valid {
			v.Value, err = parseFlag(v.Kind, override.String)
			if err != nil {
				return nil, err
			}
			v.Overridden = true
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (f *Flags) Set(ctx context.Context, id int64, name string, value flags.Value, reset bool) error {
	f.mu.Lock()
	var state flags.State
	changed := false
	err := f.DB.Tx(ctx, func(tx *sql.Tx) error {
		var kind, def string
		var old sql.NullString
		var version int64
		if err := tx.QueryRowContext(ctx, `SELECT kind,default_json,override_json,version FROM sdk_flags WHERE bot_id=? AND name=?`, id, name).Scan(&kind, &def, &old, &version); err != nil {
			if err == sql.ErrNoRows {
				return wire.Errorf(wire.CodeNotFound, "flag is not registered")
			}
			return err
		}
		if reset {
			var err error
			value, err = parseFlag(kind, def)
			if err != nil {
				return err
			}
		} else {
			var err error
			value, err = flags.Normalize(kind, value)
			if err != nil {
				return wire.Errorf(wire.CodeBadRequest, "%v", err)
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		var override any
		if !reset {
			override = string(encoded)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sdk_flags SET override_json=?,version=version+1 WHERE bot_id=? AND name=?`, override, id, name); err != nil {
			return err
		}
		previous := def
		if old.Valid {
			previous = old.String
		}
		changed = previous != string(encoded)
		if changed {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sdk_flag_changes(bot_id,name,ts) VALUES(?,?,?)`, id, name, time.Now().Unix()); err != nil {
				return err
			}
		}
		state = flags.State{Value: value, Version: version + 1, Reset: reset}
		return nil
	})
	peers := []*wire.Conn{}
	if err == nil {
		for k, c := range f.peers {
			if k.id == id {
				peers = append(peers, c)
			}
		}
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	for _, c := range peers {
		notify, cancel := context.WithTimeout(ctx, 2*time.Second)
		c.Call(notify, "sdk.flags.update", map[string]flags.State{name: state}, nil)
		cancel()
	}
	if changed && f.Changed != nil {
		f.Changed(id, name)
	}
	return nil
}

func (f *Flags) Marks(ctx context.Context, id, from int64) ([]FlagMark, error) {
	rows, err := f.DB.R.QueryContext(ctx, `SELECT ts,name FROM sdk_flag_changes WHERE bot_id=? AND ts>=? ORDER BY id DESC LIMIT 200`, id, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FlagMark{}
	for rows.Next() {
		var m FlagMark
		var name string
		if err := rows.Scan(&m.TS, &name); err != nil {
			return nil, err
		}
		m.Label = "flag " + name
		out = append(out, m)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
