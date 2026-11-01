package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/tztext"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// DefaultUnavailable is the text users get while a bot restarts.
const DefaultUnavailable = "Бот перезапускается, попробуйте через минуту"

// DefaultTZ is the time zone of texts for bot users.
const DefaultTZ = tztext.Default

// edgeSync keeps the edge's copy of settings current.
type edgeSync struct {
	mu      sync.Mutex
	version int64
	// extra lets other parts (maintenance) fill per-bot fields.
	extra []func(ctx context.Context, id int64, b *edge.BotSettings)
}

const kvEdgeVersion = "edge_settings_version"
const kvEdgeCert = "edge_cert_pem"

// EdgeSettings builds the document for the edge from core.db.
func (c *Core) EdgeSettings(ctx context.Context) (edge.Settings, error) {
	bots, err := c.Bots(ctx)
	if err != nil {
		return edge.Settings{}, err
	}
	doc := edge.Settings{Bots: map[int64]edge.BotSettings{}}
	for _, b := range bots {
		sec, err := c.Sec.Decrypt(ctx, b.ID, secrets.WebhookSecretName)
		if err != nil {
			continue
		}
		bs := edge.BotSettings{Secret: string(sec), UnavailableText: DefaultUnavailable, TZ: DefaultTZ}
		if st, err := c.Settings(ctx, b.ID); err == nil {
			var texts struct {
				Unavailable string `json:"unavailable"`
			}
			json.Unmarshal(st["texts"], &texts)
			if texts.Unavailable != "" {
				bs.UnavailableText = texts.Unavailable
			}
			var tz string
			if json.Unmarshal(st["tz"], &tz) == nil && tz != "" {
				bs.TZ = tz
			}
		}
		for _, fn := range c.edge.extra {
			fn(ctx, b.ID, &bs)
		}
		doc.Bots[b.ID] = bs
	}
	return doc, nil
}

// PushEdge sends fresh settings to every connected edge. Call it after any
// change of bots, secrets, maintenance or texts.
func (c *Core) PushEdge(ctx context.Context) {
	c.edge.mu.Lock()
	defer c.edge.mu.Unlock()
	doc, err := c.EdgeSettings(ctx)
	if err != nil {
		c.Log.Error("building edge settings failed", "err", err)
		return
	}
	c.edge.version++
	doc.Version = c.edge.version
	c.DB.W.ExecContext(ctx, `INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		kvEdgeVersion, strconv.FormatInt(c.edge.version, 10))
	for _, conn := range c.Srv.Conns() {
		if conn.Proto() != wire.ProtoEdgeCore {
			continue
		}
		var got int64
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := conn.Call(cctx, edge.MethodSettings, doc, &got)
		cancel()
		if err != nil || got != doc.Version {
			c.Log.Warn("edge did not confirm settings", "version", doc.Version, "got", got, "err", err)
		}
	}
}

// EdgeCert returns the PEM of the edge's self-signed certificate, or nil
// when the edge uses Let's Encrypt.
func (c *Core) EdgeCert(ctx context.Context) []byte {
	var v string
	if err := c.DB.R.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, kvEdgeCert).Scan(&v); err != nil {
		return nil
	}
	return []byte(v)
}

func (c *Core) registerEdgeSync() {
	var v string
	if err := c.DB.R.QueryRow(`SELECT value FROM kv WHERE key = ?`, kvEdgeVersion).Scan(&v); err == nil {
		c.edge.version, _ = strconv.ParseInt(v, 10, 64)
	} else if err != sql.ErrNoRows {
		c.Log.Warn("reading edge settings version", "err", err)
	}
	c.Mux.Handle(edge.MethodCert, wire.Func(func(ctx context.Context, conn *wire.Conn, pem []byte) (struct{}, error) {
		_, err := c.DB.W.ExecContext(ctx, `INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
			kvEdgeCert, string(pem))
		// The edge just connected: give it the current settings.
		go c.PushEdge(context.Background())
		return struct{}{}, err
	}))
}
