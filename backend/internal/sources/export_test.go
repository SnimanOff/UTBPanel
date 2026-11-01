package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

func TestBatchExportMetricsAndDownloads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Larger than a transport chunk: the reverse metrics transfer must loop.
	blob := bytes.Repeat([]byte{1, 2, 3}, 150_000)
	db.W.Exec(`INSERT INTO m_minute(bot_id,ts,data) VALUES(1,1,?),(2,1,?)`, blob, []byte("other-bot"))
	extra := &backup.Files{Dir: filepath.Join(t.TempDir(), "metrics")}
	created := &backup.Files{Dir: filepath.Join(t.TempDir(), "archives")}
	var mu sync.Mutex
	passwords := []string{}
	metricSnapshots := map[int64][]byte{}
	failSecond := false
	cm := wire.NewMux()
	cm.Handle("bots.list", wire.Func(func(context.Context, *wire.Conn, struct{}) ([]core.Bot, error) {
		return []core.Bot{{ID: 1}, {ID: 2}}, nil
	}))
	cm.Handle("config.get", wire.Func(func(_ context.Context, _ *wire.Conn, id int64) (botconfig.Snapshot, error) {
		d := botconfig.Document{}
		botconfig.Normalize(&d)
		return botconfig.Snapshot{Version: strconv.FormatInt(id, 10), Document: d}, nil
	}))
	cm.Handle("exports.bot", wire.Func(func(ctx context.Context, c *wire.Conn, in backup.ExportRequest) (backup.File, error) {
		mu.Lock()
		fail := failSecond && in.BotID == 2
		passwords = append(passwords, in.Password)
		mu.Unlock()
		if fail {
			return backup.File{}, wire.Errorf(wire.CodeBadRequest, "failed second bot")
		}
		if in.Metrics != nil {
			reader := &backup.RemoteReader{Ctx: ctx, Ref: backup.FileRef{ID: in.Metrics.ID, BotID: in.BotID}, Size: in.Metrics.Size, ReadChunk: func(ctx context.Context, ref backup.FileRef) ([]byte, error) {
				var chunk []byte
				err := c.Call(ctx, "exports.metrics.read", ref, &chunk)
				return chunk, err
			}}
			raw, err := io.ReadAll(reader)
			if err != nil {
				return backup.File{}, err
			}
			mu.Lock()
			metricSnapshots[in.BotID] = raw
			mu.Unlock()
		}
		return created.Create(ctx, in.BotID, "bot.tgbot", func(w io.Writer) error {
			_, err := w.Write(bytes.Repeat([]byte{byte(in.BotID)}, 3*backup.ChunkSize))
			return err
		})
	}))
	cm.Handle("exports.info", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) (backup.File, error) {
		return created.Info(ref)
	}))
	cm.Handle("exports.read", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) ([]byte, error) { return created.Read(ref) }))
	cm.Handle("exports.delete", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) (struct{}, error) {
		return struct{}{}, created.Delete(ref)
	}))
	run, err := os.MkdirTemp("", "export-wire-")
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
	server := &wire.Server{Mux: cm, Hello: app.Hello(wire.ProtoCorePanel, "core", 1, 1)}
	go server.Serve(ln)
	pm := wire.NewMux()
	client := &wire.Client{Path: socket, Mux: pm, Hello: app.Hello(wire.ProtoCorePanel, "panel", 1, 1)}
	go client.Run(ctx)
	wait, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	conn, err := client.Wait(wait)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rules := &alerts.Rules{S: &metrics.Storage{DB: db}}
	docs := &botconfig.Service{Core: client, Rules: rules}
	service := &Exports{Core: client, Documents: docs, Metrics: &metrics.Storage{DB: db}, Extras: extra}
	mux := api.NewMux()
	service.Register(mux, pm)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/exports/bots", strings.NewReader(`{"metrics":true}`)))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	var result Exported
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 || len(strings.Fields(result.Password)) != 6 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%+v", result)
	}
	mu.Lock()
	if len(passwords) != 2 || passwords[0] != result.Password || passwords[1] != result.Password {
		t.Fatal("batch password differs")
	}
	for id, raw := range metricSnapshots {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal("invalid metrics", err)
		}
		data := doc["m_minute"].([]any)
		if len(data) != 1 || int64(data[0].(map[string]any)["bot_id"].(float64)) != id {
			t.Fatal("cross-bot metrics")
		}
	}
	mu.Unlock()
	if entries, _ := os.ReadDir(extra.Dir); len(entries) != 0 {
		t.Fatal("plaintext metrics retained")
	}
	for _, file := range result.Files {
		download := httptest.NewRecorder()
		mux.ServeHTTP(download, httptest.NewRequest("GET", fmt.Sprintf("/api/bots/%d/exports/%s", file.BotID, file.ID), nil))
		if download.Code != 200 || !bytes.Equal(download.Body.Bytes(), bytes.Repeat([]byte{byte(file.BotID)}, 3*backup.ChunkSize)) {
			t.Fatal("bad download", download.Code)
		}
		created.Delete(backup.FileRef{ID: file.ID, BotID: file.BotID})
	}
	mu.Lock()
	failSecond = true
	mu.Unlock()
	failed := httptest.NewRecorder()
	mux.ServeHTTP(failed, httptest.NewRequest("POST", "/api/exports/bots", strings.NewReader(`{}`)))
	if failed.Code != 400 {
		t.Fatal("failure status", failed.Code, failed.Body.String())
	}
	if entries, _ := os.ReadDir(created.Dir); len(entries) != 0 {
		t.Fatal("incomplete batch retained")
	}
}
