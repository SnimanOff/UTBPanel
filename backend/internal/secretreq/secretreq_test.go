package secretreq

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func open(t *testing.T) (*Store, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{DB: db, Notes: &alerts.Service{DB: db}}, db
}

var device = api.Caller{Identity: pki.Identity{Serial: "laptop", Name: "laptop"}, Source: api.SourceMCP}

func openNotes(db *store.DB, key string) int {
	var n int
	db.R.QueryRow(`SELECT COUNT(*) FROM notifications WHERE key = ? AND resolved = 0`, key).Scan(&n)
	return n
}

func TestSecretRequests(t *testing.T) {
	ctx := context.Background()
	s, db := open(t)
	for _, bad := range []string{"BOT_TOKEN", "WEBHOOK_SECRET", "1BAD", ""} {
		if _, err := s.Ask(ctx, device, 7, bad, "", "", 0); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	r, err := s.Ask(ctx, device, 7, "PAYMENT_KEY", "ключ из кабинета ЮKassa", "", 0)
	if err != nil || r.State != "waiting" || r.Requested != "laptop" {
		t.Fatalf("%+v %v", r, err)
	}
	if again, _ := s.Ask(ctx, device, 7, "PAYMENT_KEY", "", "", 0); again.ID != r.ID {
		t.Fatal("a waiting request is asked twice")
	}
	if openNotes(db, "secret:"+r.ID) != 1 {
		t.Fatal("no notification")
	}
	woke := s.Wake()
	s.Fulfilled(ctx, 7, "PAYMENT_KEY", "owner")
	select {
	case <-woke:
	case <-time.After(time.Second):
		t.Fatal("waiters not woken")
	}
	if r, _ = s.Get(ctx, r.ID); r.State != "set" || r.DecidedBy != "owner" || openNotes(db, "secret:"+r.ID) != 0 {
		t.Fatalf("%+v", r)
	}

	r, _ = s.Ask(ctx, device, 7, "SMTP_PASSWORD", "", "", 0)
	if err := s.Reject(ctx, r.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.Reject(ctx, r.ID, "owner"); !errors.Is(err, ErrDecided) {
		t.Fatalf("second reject: %v", err)
	}
	r, _ = s.Ask(ctx, device, 7, "OLD", "", "", 0)
	db.W.Exec(`UPDATE secret_requests SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Second).Unix(), r.ID)
	if r, _ = s.Get(ctx, r.ID); r.State != "expired" {
		t.Fatalf("%+v", r)
	}
	if list, _ := s.List(ctx, 7, true); len(list) != 0 {
		t.Fatalf("waiting %+v", list)
	}
	if list, _ := s.List(ctx, 7, false); len(list) != 3 {
		t.Fatalf("history %+v", list)
	}
}

func TestNewBotToken(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	r, err := s.Ask(ctx, device, 0, "", "у @BotFather", "Shop", 2)
	if err != nil || r.Name != TokenName || r.BotName != "Shop" || r.GroupID != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	var created []string
	a := &API{Store: s, WriteError: func(w http.ResponseWriter, err error) { api.WriteError(w, 422, "telegram", err.Error()) },
		CreateBot: func(_ context.Context, name, token string, group int64) (int64, string, error) {
			if token != "111:good" {
				return 0, "", errors.New("Unauthorized")
			}
			created = append(created, name)
			return 42, "shop_bot", nil
		}}
	mux := api.NewMux()
	a.Register(mux)
	post := func(caller api.Caller, path, body string) int {
		req := httptest.NewRequest("POST", path, bytes.NewBufferString(body)).WithContext(api.WithCaller(ctx, caller))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	owner := api.Caller{Identity: pki.Identity{Serial: "o", Name: "owner", Kind: pki.Owner}, Source: api.SourceUI}
	path := "/api/secret-requests/" + r.ID + "/token"
	if code := post(device, path, `{"token":"111:good"}`); code != 403 {
		t.Fatalf("MCP answered its own request: %d", code)
	}
	if code := post(owner, path, `{"token":"bad"}`); code != 422 {
		t.Fatalf("bad token: %d", code)
	}
	if r, _ = s.Get(ctx, r.ID); r.State != "waiting" {
		t.Fatal("a refused token must keep the request")
	}
	if code := post(owner, path, `{"token":"111:good"}`); code != 201 {
		t.Fatalf("token: %d", code)
	}
	if r, _ = s.Get(ctx, r.ID); r.State != "set" || r.CreatedBot != 42 || len(created) != 1 || created[0] != "Shop" {
		t.Fatalf("%+v %v", r, created)
	}
	if code := post(owner, path, `{"token":"111:good"}`); code != 409 {
		t.Fatalf("second token: %d", code)
	}
}
