package bots

import (
	"context"
	"database/sql"
	"errors"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"strings"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// Group is a row of bots on the main page.
type Group struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
	MCPMode  string `json:"mcp_mode"` // off | read | full
}

// Meta is where a bot sits on the main page.
type Meta struct {
	GroupID  int64 `json:"group_id,omitempty"` // 0 = no group
	Position int   `json:"position"`
}

// Groups keeps groups and bot order in panel.db.
type Groups struct {
	DB    *store.DB
	Guard botconfig.Guard
}

// ErrGroupName is returned for empty or duplicate names.
var ErrGroupName = errors.New("group name must be unique and not empty")

// List returns groups in order.
func (g *Groups) List(ctx context.Context) ([]Group, error) {
	rows, err := g.DB.R.QueryContext(ctx, `SELECT id, name, position, mcp_mode FROM groups ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var gr Group
		if err := rows.Scan(&gr.ID, &gr.Name, &gr.Position, &gr.MCPMode); err != nil {
			return nil, err
		}
		out = append(out, gr)
	}
	return out, rows.Err()
}

// Create adds a group at the end.
func (g *Groups) Create(ctx context.Context, name string) (Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Group{}, ErrGroupName
	}
	res, err := g.DB.W.ExecContext(ctx, `INSERT INTO groups (name, position) VALUES (?, (SELECT COALESCE(MAX(position), -1) + 1 FROM groups))`, name)
	if err != nil {
		return Group{}, ErrGroupName
	}
	id, _ := res.LastInsertId()
	var gr Group
	err = g.DB.W.QueryRowContext(ctx, `SELECT id, name, position, mcp_mode FROM groups WHERE id = ?`, id).Scan(&gr.ID, &gr.Name, &gr.Position, &gr.MCPMode)
	return gr, err
}

// Rename changes a group name.
func (g *Groups) Rename(ctx context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrGroupName
	}
	res, err := g.DB.W.ExecContext(ctx, `UPDATE groups SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return ErrGroupName
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete removes a group; its bots move to "No group".
func (g *Groups) Delete(ctx context.Context, id int64) error {
	ctx, unlock, err := g.Guard.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return g.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE bot_meta SET group_id = NULL WHERE group_id = ?`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// Order sets group positions by the order of ids.
func (g *Groups) Order(ctx context.Context, ids []int64) error {
	return g.DB.Tx(ctx, func(tx *sql.Tx) error {
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE groups SET position = ? WHERE id = ?`, i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Metas returns the place of every bot.
func (g *Groups) Metas(ctx context.Context) (map[int64]Meta, error) {
	rows, err := g.DB.R.QueryContext(ctx, `SELECT bot_id, COALESCE(group_id, 0), position FROM bot_meta`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Meta{}
	for rows.Next() {
		var id int64
		var m Meta
		if err := rows.Scan(&id, &m.GroupID, &m.Position); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}

func nullGroup(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// Place puts the bots ids into a group (0 = none) in this order. Bots that
// came from another group are moved.
func (g *Groups) Place(ctx context.Context, groupID int64, ids []int64) error {
	ctx, unlock, err := g.Guard.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return g.DB.Tx(ctx, func(tx *sql.Tx) error {
		if groupID > 0 {
			var n int
			tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM groups WHERE id = ?`, groupID).Scan(&n)
			if n == 0 {
				return sql.ErrNoRows
			}
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT INTO bot_meta (bot_id, group_id, position) VALUES (?, ?, ?)
				ON CONFLICT (bot_id) DO UPDATE SET group_id = excluded.group_id, position = excluded.position`, id, nullGroup(groupID), i); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetGroup moves one bot to the end of a group.
func (g *Groups) SetGroup(ctx context.Context, botID, groupID int64) error {
	ctx, unlock, err := g.Guard.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return g.DB.Tx(ctx, func(tx *sql.Tx) error {
		if groupID > 0 {
			var n int
			tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM groups WHERE id = ?`, groupID).Scan(&n)
			if n == 0 {
				return sql.ErrNoRows
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO bot_meta (bot_id, group_id, position)
			VALUES (?, ?, (SELECT COALESCE(MAX(position), -1) + 1 FROM bot_meta WHERE group_id IS ?))
			ON CONFLICT (bot_id) DO UPDATE SET group_id = excluded.group_id, position = excluded.position`, botID, nullGroup(groupID), nullGroup(groupID))
		return err
	})
}

func (g *Groups) SetMCPMode(ctx context.Context, id int64, mode string) (string, error) {
	if mode != "off" && mode != "read" && mode != "full" {
		return "", errors.New("invalid MCP mode")
	}
	ctx, unlock, err := g.Guard.Lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	var previous string
	err = g.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT mcp_mode FROM groups WHERE id=?`, id).Scan(&previous); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE groups SET mcp_mode=? WHERE id=?`, mode, id)
		return err
	})
	return previous, err
}

// Forget removes a deleted bot.
func (g *Groups) Forget(ctx context.Context, botID int64) error {
	ctx, unlock, err := g.Guard.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = g.DB.W.ExecContext(ctx, `DELETE FROM bot_meta WHERE bot_id = ?`, botID)
	return err
}
