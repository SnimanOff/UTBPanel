package core

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Static pages. A page is one HTML file (CSS and JS inline) that the edge
// serves at https://<sub>.<DOMAIN>:<webhook port>/ straight from memory:
// no bot, no core round trip, and it keeps working while the core
// restarts. The edge has little memory, so pages are small and few.

// Bounds of pages.
const (
	MaxPageSize   = 512 << 10 // one page
	MaxPagesTotal = 4 << 20   // all pages together
	MaxPages      = 20
)

// PageInfo describes a page without its body.
type PageInfo struct {
	Sub       string `cbor:"1,keyasint" json:"sub"`
	Size      int    `cbor:"2,keyasint" json:"size"`
	UpdatedAt string `cbor:"3,keyasint" json:"updated_at"`
	URL       string `cbor:"4,keyasint" json:"url"`
}

// Page is a page with its HTML.
type Page struct {
	PageInfo `cbor:"1,keyasint"`
	HTML     string `cbor:"2,keyasint" json:"html"`
}

// PageReq creates, replaces or deletes a page. Old is the label the page
// had before a rename ("" = new page or same label).
type PageReq struct {
	Sub   string        `cbor:"1,keyasint"`
	HTML  string        `cbor:"2,keyasint,omitempty"`
	Old   string        `cbor:"3,keyasint,omitempty"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

// publicURL is https://<host>[:port] on the edge's public port.
func (c *Core) publicURL(host string) string {
	if host == "" {
		return ""
	}
	if c.Cfg.WebhookPort != 0 && c.Cfg.WebhookPort != 443 {
		host = net.JoinHostPort(host, strconv.Itoa(c.Cfg.WebhookPort))
	}
	return "https://" + host
}

// Pages lists the pages, sorted by label.
func (c *Core) Pages(ctx context.Context) ([]PageInfo, error) {
	rows, err := c.DB.R.QueryContext(ctx, `SELECT sub, length(html), updated_at FROM pages ORDER BY sub`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PageInfo{}
	for rows.Next() {
		var p PageInfo
		if err := rows.Scan(&p.Sub, &p.Size, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.URL = c.publicURL(c.miniAppHost(p.Sub))
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPage returns one page with its HTML.
func (c *Core) GetPage(ctx context.Context, sub string) (Page, error) {
	var p Page
	var html []byte
	err := c.DB.R.QueryRowContext(ctx, `SELECT sub, html, updated_at FROM pages WHERE sub = ?`, sub).Scan(&p.Sub, &html, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wire.Errorf(wire.CodeNotFound, "нет страницы %q", sub)
	}
	if err != nil {
		return p, err
	}
	p.HTML, p.Size = string(html), len(html)
	p.URL = c.publicURL(c.miniAppHost(p.Sub))
	return p, nil
}

// pageTaken reports whether a page has the label.
func (c *Core) pageTaken(ctx context.Context, sub string) bool {
	var n int
	c.DB.R.QueryRowContext(ctx, `SELECT count(*) FROM pages WHERE sub = ?`, sub).Scan(&n)
	return n > 0
}

// PutPage creates or replaces a page; with Old set it also renames it.
func (c *Core) PutPage(ctx context.Context, in PageReq) error {
	in.Sub = strings.ToLower(strings.TrimSpace(in.Sub))
	if in.Old == in.Sub {
		in.Old = ""
	}
	if in.Sub == "" {
		return wire.Errorf(wire.CodeBadRequest, "укажите поддомен, например test")
	}
	if err := checkSubdomain(in.Sub); err != nil {
		return err
	}
	if c.Cfg.Domain == "" {
		return wire.Errorf(wire.CodeBadRequest, "для страниц нужен домен: задайте DOMAIN в .env")
	}
	if strings.TrimSpace(in.HTML) == "" {
		return wire.Errorf(wire.CodeBadRequest, "страница пустая")
	}
	if len(in.HTML) > MaxPageSize {
		return wire.Errorf(wire.CodeBadRequest, "страница больше %d КБ", MaxPageSize>>10)
	}
	if !utf8.ValidString(in.HTML) {
		return wire.Errorf(wire.CodeBadRequest, "страница должна быть в UTF-8")
	}
	var bot int64
	if err := c.DB.R.QueryRowContext(ctx, `SELECT id FROM bots WHERE json_extract(settings, '$.mini_app') = ?`, in.Sub).Scan(&bot); err == nil {
		return wire.Errorf("conflict", "поддомен %q занят Mini App бота #%d", in.Sub, bot)
	}
	if in.Old != "" && !c.pageTaken(ctx, in.Old) {
		return wire.Errorf(wire.CodeNotFound, "нет страницы %q", in.Old)
	}
	if in.Old != "" && c.pageTaken(ctx, in.Sub) {
		return wire.Errorf("conflict", "страница %q уже есть", in.Sub)
	}
	// Bounds over all pages, without the one being replaced.
	var n, total int
	c.DB.R.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(length(html)), 0) FROM pages WHERE sub NOT IN (?, ?)`, in.Sub, in.Old).Scan(&n, &total)
	if n+1 > MaxPages {
		return wire.Errorf(wire.CodeBadRequest, "страниц не больше %d", MaxPages)
	}
	if total+len(in.HTML) > MaxPagesTotal {
		return wire.Errorf(wire.CodeBadRequest, "все страницы вместе не больше %d МБ", MaxPagesTotal>>20)
	}
	tx, err := c.DB.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if in.Old != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM pages WHERE sub = ?`, in.Old); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pages (sub, html, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (sub) DO UPDATE SET html = excluded.html, updated_at = excluded.updated_at`,
		in.Sub, []byte(in.HTML), store.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

// DeletePage removes a page.
func (c *Core) DeletePage(ctx context.Context, sub string) error {
	res, err := c.DB.W.ExecContext(ctx, `DELETE FROM pages WHERE sub = ?`, sub)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return wire.Errorf(wire.CodeNotFound, "нет страницы %q", sub)
	}
	return nil
}

// edgePages is host → HTML for the edge's settings.
func (c *Core) edgePages(ctx context.Context) map[string][]byte {
	if c.Cfg.Domain == "" {
		return nil
	}
	rows, err := c.DB.R.QueryContext(ctx, `SELECT sub, html FROM pages`)
	if err != nil {
		c.Log.Warn("reading pages for edge", "err", err)
		return nil
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var sub string
		var html []byte
		if rows.Scan(&sub, &html) == nil {
			out[c.miniAppHost(sub)] = html
		}
	}
	return out
}

func (c *Core) registerPages() {
	c.Mux.Handle("pages.list", wire.Func(func(ctx context.Context, _ *wire.Conn, _ struct{}) ([]PageInfo, error) {
		return c.Pages(ctx)
	}))
	c.Mux.Handle("pages.get", wire.Func(func(ctx context.Context, _ *wire.Conn, sub string) (Page, error) {
		return c.GetPage(ctx, sub)
	}))
	c.Mux.Handle("pages.put", wire.Func(func(ctx context.Context, _ *wire.Conn, in PageReq) (PageInfo, error) {
		err := c.Journal.Do(ctx, in.Actor, "pages.put", 0, map[string]any{"sub": in.Sub, "old": in.Old, "size": len(in.HTML)}, func() error {
			return c.PutPage(ctx, in)
		})
		if err != nil {
			return PageInfo{}, err
		}
		c.PushEdge(ctx)
		p, err := c.GetPage(ctx, strings.ToLower(strings.TrimSpace(in.Sub)))
		return p.PageInfo, err
	}))
	c.Mux.Handle("pages.delete", wire.Func(func(ctx context.Context, _ *wire.Conn, in PageReq) (struct{}, error) {
		err := c.Journal.Do(ctx, in.Actor, "pages.delete", 0, map[string]any{"sub": in.Sub}, func() error {
			return c.DeletePage(ctx, in.Sub)
		})
		if err == nil {
			c.PushEdge(ctx)
		}
		return struct{}{}, err
	}))
}
