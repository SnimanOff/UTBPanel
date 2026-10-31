package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// fakeTelegram knows a set of valid tokens and records calls.
type fakeTelegram struct {
	mu      sync.Mutex
	tokens  map[string]string // token → "id:username"
	calls   []string
	hookURL string
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/bot"), "/", 2)
	token, method := parts[0], parts[1]
	f.mu.Lock()
	f.calls = append(f.calls, method+" "+token)
	who, ok := f.tokens[token]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(401)
		io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	id, user, _ := strings.Cut(who, ":")
	if method == "getWebhookInfo" {
		f.mu.Lock()
		u := f.hookURL
		f.mu.Unlock()
		io.WriteString(w, `{"ok":true,"result":{"url":"`+u+`","pending_update_count":0}}`)
		return
	}
	if method == "setWebhook" {
		f.mu.Lock()
		f.hookURL = "ours"
		f.mu.Unlock()
	}
	if method == "getMe" {
		io.WriteString(w, `{"ok":true,"result":{"id":`+id+`,"is_bot":true,"first_name":"B","username":"`+user+`"}}`)
		return
	}
	io.WriteString(w, `{"ok":true,"result":true}`)
}

func withTelegram(t *testing.T, c *Core) *fakeTelegram {
	f := &fakeTelegram{tokens: map[string]string{"111:good": "111:echo_bot", "222:other": "222:other_bot"}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c.TG.Base = srv.URL
	return f
}

func TestCreateBot(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)

	_, err := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:bad"})
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != CodeTelegram || we.Message != "Unauthorized" {
		t.Fatalf("bad token: %v", err)
	}
	if bots, _ := c.Bots(ctx); len(bots) != 0 {
		t.Fatal("bot saved despite a bad token")
	}

	b, err := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: " 111:good "})
	if err != nil {
		t.Fatal(err)
	}
	if b.Username != "echo_bot" || b.TgID != 111 || b.State != StateStopped || b.Port < c.Cfg.BotPortFirst {
		t.Fatalf("bot = %+v", b)
	}
	tok, err := c.Token(ctx, b.ID)
	if err != nil || tok != "111:good" {
		t.Fatalf("token = %q %v", tok, err)
	}
	if ws, err := c.Sec.Decrypt(ctx, b.ID, secrets.WebhookSecretName); err != nil || len(ws) < 32 {
		t.Fatalf("webhook secret: %v", err)
	}
	if _, err := c.CreateBot(ctx, CreateBot{Name: "Echo2", Token: "111:good"}); !errors.As(err, &we) || we.Code != "exists" {
		t.Fatalf("duplicate: %v", err)
	}
	b2, _ := c.CreateBot(ctx, CreateBot{Name: "Other", Token: "222:other"})
	if b2.Port == b.Port || b2.Port-b.Port != 2 {
		t.Fatalf("ports %d and %d", b.Port, b2.Port)
	}
}

func TestSetToken(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	f := withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})

	if _, err := c.SetToken(ctx, SetToken{BotID: b.ID, Token: "nope"}); err == nil {
		t.Fatal("bad token accepted")
	}
	if tok, _ := c.Token(ctx, b.ID); tok != "111:good" {
		t.Fatal("token changed after a failed save")
	}
	// A running bot: the old token's webhook is removed.
	c.DB.W.Exec(`UPDATE bots SET state = 'running' WHERE id = ?`, b.ID)
	nb, err := c.SetToken(ctx, SetToken{BotID: b.ID, Token: "222:other"})
	if err != nil || nb.Username != "other_bot" {
		t.Fatalf("%+v %v", nb, err)
	}
	found := false
	for _, call := range f.calls {
		found = found || call == "deleteWebhook 111:good"
	}
	if !found {
		t.Fatalf("old webhook not removed: %v", f.calls)
	}
}
