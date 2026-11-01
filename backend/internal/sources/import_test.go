package sources

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func TestImportHTTPUploadsChunksAndRestoresMetricsBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	blob := bytes.Repeat([]byte{1, 2}, 200_000)
	history := []byte(`{"version":1,"m_minute":[{"bot_id":999,"ts":1,"data":"` + base64.StdEncoding.EncodeToString(blob) + `"}],"m_hour":[],"m_day":[],"m_uniq":[]}`)
	doc := botconfig.Document{Secrets: []string{"KEY"}}
	botconfig.Normalize(&doc)
	preview := backup.Preview{ID: "session", Document: doc, Metrics: true, Manifest: backup.Manifest{Name: "imported", Username: "imported", CurrentVersion: "v1", Entries: map[string]backup.Entry{"metrics/history.json": {Size: int64(len(history))}}}}
	var mu sync.Mutex
	var uploaded bytes.Buffer
	reads := 0
	aborted := false
	started := false
	cm := wire.NewMux()
	cm.Handle("imports.begin", wire.Func(func(context.Context, *wire.Conn, struct{}) (string, error) { return "session", nil }))
	cm.Handle("imports.chunk", wire.Func(func(_ context.Context, _ *wire.Conn, in backup.ImportChunk) (struct{}, error) {
		mu.Lock()
		defer mu.Unlock()
		if in.Offset != int64(uploaded.Len()) || len(in.Data) > backup.ChunkSize {
			return struct{}{}, wire.Errorf(wire.CodeBadRequest, "bad chunk")
		}
		uploaded.Write(in.Data)
		return struct{}{}, nil
	}))
	cm.Handle("imports.inspect", wire.Func(func(_ context.Context, _ *wire.Conn, in backup.ImportInspect) (backup.Preview, error) {
		if in.Password != "archive password" {
			return backup.Preview{}, wire.Errorf(wire.CodeBadRequest, "wrong password")
		}
		return preview, nil
	}))
	cm.Handle("imports.preview", wire.Func(func(context.Context, *wire.Conn, string) (backup.Preview, error) { return preview, nil }))
	cm.Handle("imports.apply", wire.Func(func(_ context.Context, _ *wire.Conn, in backup.ImportApply) (core.Bot, error) {
		if in.Token != "123:private" || in.Secrets["KEY"].Kind != "file" || !bytes.Equal(in.Secrets["KEY"].Value, []byte{0, 1}) {
			return core.Bot{}, wire.Errorf(wire.CodeBadRequest, "credentials lost")
		}
		return core.Bot{ID: 3, Name: "imported", State: core.StateStopped}, nil
	}))
	cm.Handle("imports.read", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) ([]byte, error) {
		mu.Lock()
		reads++
		mu.Unlock()
		return history[ref.Offset:min(int64(len(history)), ref.Offset+backup.ChunkSize)], nil
	}))
	cm.Handle("imports.abort", wire.Func(func(context.Context, *wire.Conn, string) (struct{}, error) {
		mu.Lock()
		aborted = true
		mu.Unlock()
		return struct{}{}, nil
	}))
	cm.Handle("bots.start", wire.Func(func(context.Context, *wire.Conn, core.BotRef) (struct{}, error) {
		var got []byte
		if err := db.R.QueryRow(`SELECT data FROM m_minute WHERE bot_id=3 AND ts=1`).Scan(&got); err != nil || !bytes.Equal(got, blob) {
			return struct{}{}, wire.Errorf(wire.CodeBadRequest, "metrics must be restored before start")
		}
		mu.Lock()
		started = true
		mu.Unlock()
		return struct{}{}, nil
	}))
	cm.Handle("bots.get", wire.Func(func(context.Context, *wire.Conn, int64) (core.Bot, error) {
		return core.Bot{ID: 3, Name: "imported", State: core.StateRunning}, nil
	}))
	run, err := os.MkdirTemp("", "import-wire-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	socket := filepath.Join(run, "s.sock")
	ln, err := wire.Listen(socket, 0o600, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go (&wire.Server{Mux: cm, Hello: app.Hello(wire.ProtoCorePanel, "core", 1, 1)}).Serve(ln)
	client := &wire.Client{Path: socket, Mux: wire.NewMux(), Hello: app.Hello(wire.ProtoCorePanel, "panel", 1, 1)}
	go client.Run(ctx)
	wait, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	conn, err := client.Wait(wait)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	extra := &backup.Files{Dir: filepath.Join(t.TempDir(), "extra")}
	service := &Exports{Core: client, Documents: &botconfig.Service{Core: client, Rules: &alerts.Rules{S: &metrics.Storage{DB: db}}}, Metrics: &metrics.Storage{DB: db}, Extras: extra}
	mux := api.NewMux()
	service.Register(mux, wire.NewMux())
	data := bytes.Repeat([]byte("encrypted archive"), 40_000)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("password", "archive password")
	part, _ := form.CreateFormFile("file", "../../untrusted.tgbot")
	part.Write(data)
	form.Close()
	request := httptest.NewRequest("POST", "/api/imports/bots", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "archive password") {
		t.Fatal(response.Code, response.Body.String())
	}
	mu.Lock()
	if !bytes.Equal(uploaded.Bytes(), data) {
		t.Fatal("upload corrupted")
	}
	mu.Unlock()
	apply := httptest.NewRecorder()
	mux.ServeHTTP(apply, httptest.NewRequest("POST", "/api/imports/bots/session/apply", strings.NewReader(`{"name":"imported","token":"123:private","start":true,"secrets":{"KEY":{"kind":"file","value":"AAE="}}}`)))
	if apply.Code != 201 {
		t.Fatal(apply.Code, apply.Body.String())
	}
	var result Imported
	if err := json.Unmarshal(apply.Body.Bytes(), &result); err != nil || !result.Started || len(result.Warnings) != 0 {
		t.Fatal(result, err)
	}
	mu.Lock()
	if reads < 2 || !aborted || !started {
		t.Fatal(reads, aborted, started)
	}
	mu.Unlock()
	if strings.Contains(apply.Body.String(), "123:private") {
		t.Fatal("token leaked")
	}
	if entries, _ := os.ReadDir(extra.Dir); len(entries) != 0 {
		t.Fatal("plaintext metrics retained")
	}
	var other int
	db.R.QueryRow(`SELECT COUNT(*) FROM m_minute WHERE bot_id=999`).Scan(&other)
	if other != 0 {
		t.Fatal("archive bot id reused")
	}
}
