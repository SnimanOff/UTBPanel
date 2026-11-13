package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Mini Apps. A bot with the settings section mini_app = "lotus-sup" serves
// its web pages at https://lotus-sup.<DOMAIN>:<webhook port>. The edge
// takes the browser's request and hands it here; the core asks the bot on
// 127.0.0.1:PORT, as it does with webhooks. The bot gets the address in
// MINI_APP_URL and must check Telegram's initData itself.

// sectionMiniApp is the settings key with the subdomain label.
const sectionMiniApp = "mini_app"

// EnvMiniAppURL is the variable with the public address of the Mini App.
const EnvMiniAppURL = "MINI_APP_URL"

var subdomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// reservedSubdomains could be mistaken for the panel or mail of the domain.
var reservedSubdomains = map[string]bool{
	"www": true, "api": true, "panel": true, "admin": true, "mail": true, "smtp": true, "imap": true,
	"pop": true, "mx": true, "ns": true, "ns1": true, "ns2": true, "ftp": true, "webmail": true,
	"autodiscover": true, "autoconfig": true, "status": true, "edge": true, "core": true,
}

// checkSubdomain checks the label alone (empty = no Mini App).
func checkSubdomain(sub string) error {
	if sub == "" {
		return nil
	}
	if !subdomainRe.MatchString(sub) || strings.Contains(sub, "--") {
		return wire.Errorf(wire.CodeBadRequest, "поддомен: латиница в нижнем регистре, цифры и дефис, до 63 символов, например lotus-sup")
	}
	if reservedSubdomains[sub] {
		return wire.Errorf(wire.CodeBadRequest, "поддомен %q зарезервирован", sub)
	}
	return nil
}

// checkMiniApp checks a subdomain for a bot: the panel needs a domain and
// no other bot may have the same subdomain.
func (c *Core) checkMiniApp(ctx context.Context, botID int64, sub string) error {
	if err := checkSubdomain(sub); err != nil {
		return err
	}
	if sub == "" {
		return nil
	}
	if c.Cfg.Domain == "" {
		return wire.Errorf(wire.CodeBadRequest, "для Mini App нужен домен: задайте DOMAIN в .env")
	}
	var other int64
	err := c.DB.R.QueryRowContext(ctx, `SELECT id FROM bots WHERE json_extract(settings, '$.mini_app') = ? AND id != ?`, sub, botID).Scan(&other)
	if err == nil {
		return wire.Errorf("conflict", "поддомен %q уже у бота #%d", sub, other)
	}
	if c.pageTaken(ctx, sub) {
		return wire.Errorf("conflict", "поддомен %q занят страницей", sub)
	}
	return nil
}

// miniAppSub returns the bot's subdomain label, "" when it has none.
func (c *Core) miniAppSub(ctx context.Context, id int64) string {
	m, err := c.Settings(ctx, id)
	if err != nil {
		return ""
	}
	var sub string
	json.Unmarshal(m[sectionMiniApp], &sub)
	return sub
}

// miniAppHost is <sub>.<domain>, "" without a domain or subdomain.
func (c *Core) miniAppHost(sub string) string {
	if sub == "" || c.Cfg.Domain == "" {
		return ""
	}
	return sub + "." + strings.ToLower(c.Cfg.Domain)
}

// MiniAppURL is the public address of the bot's Mini App, "" when it has none.
func (c *Core) MiniAppURL(ctx context.Context, id int64) string {
	return c.publicURL(c.miniAppHost(c.miniAppSub(ctx, id)))
}

// appClient asks bots for Mini App pages: no redirects followed (the browser
// gets them), no proxy from the environment.
var appClient = &http.Client{
	Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 4, IdleConnTimeout: time.Minute,
		ResponseHeaderTimeout: edge.AppTimeout, DisableCompression: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       edge.AppTimeout,
}

func appError(status int, text string) edge.AppResponse {
	return edge.AppResponse{Status: status, Header: map[string][]string{"Content-Type": {"text/plain; charset=utf-8"}, "Cache-Control": {"no-store"}},
		Body: []byte(text)}
}

// serveApp passes one Mini App request to the bot.
func (c *Core) serveApp(ctx context.Context, in edge.AppRequest) edge.AppResponse {
	// The edge's copy of settings may be older: check again here.
	host := c.miniAppHost(c.miniAppSub(ctx, in.BotID))
	if host == "" || host != in.Host {
		return appError(http.StatusNotFound, "not found")
	}
	if !strings.HasPrefix(in.URI, "/") {
		return appError(http.StatusBadRequest, "bad request")
	}
	t, err := c.target(ctx, in.BotID)
	if err != nil {
		return appError(http.StatusNotFound, "not found")
	}
	if !t.Running {
		return appError(http.StatusServiceUnavailable, "Бот не запущен")
	}
	if len(in.Body) > edge.MaxAppRequest {
		return appError(http.StatusRequestEntityTooLarge, "too large")
	}
	ctx, cancel := context.WithTimeout(ctx, edge.AppTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, in.Method, "http://127.0.0.1:"+strconv.Itoa(t.Port)+in.URI, bytes.NewReader(in.Body))
	if err != nil {
		return appError(http.StatusBadRequest, "bad request")
	}
	for k, v := range in.Header {
		req.Header[k] = v
	}
	req.Host = in.Host
	req.Header.Set("X-Forwarded-For", in.IP)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", in.Host)
	resp, err := appClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return appError(http.StatusGatewayTimeout, "Бот не ответил вовремя")
		}
		return appError(http.StatusBadGateway, "Бот не отвечает")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, edge.MaxAppResponse+1))
	if err != nil {
		return appError(http.StatusBadGateway, "Бот оборвал ответ")
	}
	if len(body) > edge.MaxAppResponse {
		c.Log.Warn("mini app response too large", "bot", in.BotID, "uri", in.URI)
		return appError(http.StatusBadGateway, "Ответ бота больше 2 МБ")
	}
	return edge.AppResponse{Status: resp.StatusCode, Header: edge.CleanHeader(resp.Header), Body: body}
}

func (c *Core) registerMiniApp() {
	c.Mux.Handle(edge.MethodApp, wire.Func(func(ctx context.Context, _ *wire.Conn, in edge.AppRequest) (edge.AppResponse, error) {
		return c.serveApp(ctx, in), nil
	}))
	// The edge routes the host and gets a certificate for it.
	c.edge.extra = append(c.edge.extra, func(ctx context.Context, id int64, b *edge.BotSettings) {
		b.App = c.miniAppHost(c.miniAppSub(ctx, id))
	})
}
