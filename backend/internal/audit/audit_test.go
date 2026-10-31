package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func TestMiddleware(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w := store.NewWriter(db, time.Hour, nil)
	defer w.Close()
	l := &Log{DB: db, W: w}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/bots/{id}/secrets", func(w http.ResponseWriter, r *http.Request) {
		Annotate(r, "secret.set", "PAYMENT_KEY", map[string]any{"value": "изменено"})
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /api/bots/{id}/stop", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	mux.HandleFunc("GET /api/bots", func(w http.ResponseWriter, r *http.Request) {})
	h := l.Middleware(mux)
	do := func(method, path string) {
		r := httptest.NewRequest(method, path, nil)
		r = r.WithContext(api.WithCaller(r.Context(), api.Caller{Identity: pki.Identity{Name: "home-pc"}, IP: "10.0.0.1", Source: api.SourceUI}))
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	do("POST", "/api/bots/3/secrets")
	do("POST", "/api/bots/3/stop")
	do("GET", "/api/bots")
	es, err := l.List(ctx, Query{})
	if err != nil || len(es) != 2 {
		t.Fatalf("%+v %v", es, err)
	}
	stop, sec := es[0], es[1]
	if sec.Action != "secret.set" || sec.Key != "home-pc" || sec.BotID != 3 || sec.Object != "PAYMENT_KEY" || string(sec.Details) != `{"value":"изменено"}` {
		t.Fatalf("secret entry = %+v", sec)
	}
	if stop.Result != "error 500" || stop.Action != "POST /api/bots/3/stop" {
		t.Fatalf("stop entry = %+v", stop)
	}
	if es, _ := l.List(ctx, Query{BotID: 3, Action: "secret"}); len(es) != 1 {
		t.Fatal("filter")
	}
}
