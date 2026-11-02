package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func TestFullBackupKeepsMasterKeyInCore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := configCore(t)
	if err := c.Sec.Set(ctx, 1, "KEY", secrets.KindValue, []byte("private-bot-secret")); err != nil {
		t.Fatal(err)
	}
	var panelPart bytes.Buffer
	panelDir := t.TempDir()
	panelDB, err := store.Open(ctx, filepath.Join(panelDir, "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer panelDB.Close()
	if _, err := pki.Open(ctx, filepath.Join(panelDir, "pki"), panelDB); err != nil {
		t.Fatal(err)
	}
	if err := dbgate.Snapshot(ctx, panelDB.Path, filepath.Join(panelDir, "panel-snapshot.db"), -1); err != nil {
		t.Fatal(err)
	}
	panelRoot, err := os.OpenRoot(panelDir)
	if err != nil {
		t.Fatal(err)
	}
	defer panelRoot.Close()
	if err := backup.Parts(ctx, &panelPart, func(a *backup.Archive) error {
		if err := a.RootFile(panelRoot, "panel-snapshot.db", "panel.db", false); err != nil {
			return err
		}
		return a.Tree(panelRoot, "pki", "pki")
	}); err != nil {
		t.Fatal(err)
	}
	masterText, err := os.ReadFile(secrets.MasterKeyPath(c.Cfg.KeysDir))
	if err != nil {
		t.Fatal(err)
	}
	master, err := secrets.ParseMasterKey(masterText)
	if err != nil {
		t.Fatal(err)
	}
	pm := wire.NewMux()
	var released atomic.Int64
	pm.Handle("backups.panel.prepare", wire.Func(func(_ context.Context, _ *wire.Conn, _ struct{}) (backup.PanelPart, error) {
		return backup.PanelPart{File: backup.File{ID: "part", Size: int64(panelPart.Len())}, Version: "panel test"}, nil
	}))
	pm.Handle("backups.panel.read", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) ([]byte, error) {
		return panelPart.Bytes()[ref.Offset:min(int64(panelPart.Len()), ref.Offset+backup.ChunkSize)], nil
	}))
	pm.Handle("backups.panel.release", wire.Func(func(_ context.Context, _ *wire.Conn, _ backup.FileRef) (struct{}, error) {
		released.Add(1)
		return struct{}{}, nil
	}))
	run, err := os.MkdirTemp("", "full-wire-")
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
	go (&wire.Server{Mux: c.Mux, Hello: app.Hello(wire.ProtoCorePanel, "core", 1, 1)}).Serve(ln)
	client := &wire.Client{Path: socket, Mux: pm, Hello: app.Hello(wire.ProtoCorePanel, "panel", 1, 1)}
	go client.Run(ctx)
	wait, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	conn, err := client.Wait(wait)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cfg := backup.DefaultFullSettings()
	cfg.Keep = 1
	if err := c.setFullSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	var file backup.File
	for range 2 {
		if err := client.Call(ctx, "backups.make", FullBackupRequest{}, &file); err != nil {
			t.Fatal(err)
		}
		if file.Verification == nil || file.Verification.State != "passed" {
			t.Fatal("archive was not verified", file)
		}
	}
	status, err := c.fullStatus(ctx)
	if err != nil || len(status.Files) != 1 || released.Load() != 2 {
		t.Fatal(status, released.Load(), err)
	}
	f, err := c.FullBackups.Open(backup.FileRef{ID: file.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader, err := backup.NewChunkReader(f, master)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(decoded, masterText) || bytes.Contains(decoded, master) {
		t.Fatal("master key leaked into backup")
	}
	gz, err := gzip.NewReader(bytes.NewReader(decoded))
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(gz)
	entries := map[string][]byte{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = raw
	}
	if len(entries["core.db"]) == 0 || len(entries["pki/ca.key"]) == 0 || len(entries["panel.db"]) == 0 {
		t.Fatal("full backup lacks data")
	}
	for name, raw := range entries {
		if bytes.Contains(raw, master) || bytes.Contains(raw, bytes.TrimSpace(masterText)) {
			t.Fatal("master key included in", name)
		}
	}
	var manifest backup.FullManifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil || manifest.Format != "utbpanel.full" || len(manifest.Bots) != 1 || manifest.Versions["panel"] != "panel test" {
		t.Fatal(manifest, err)
	}
	if err := client.Call(ctx, "backups.downloaded", backup.FileRef{ID: file.ID}, nil); err != nil {
		t.Fatal(err)
	}
	status, err = c.fullStatus(ctx)
	if err != nil || status.LastDownload.IsZero() || status.Files[0].DownloadedAt.IsZero() {
		t.Fatal(status, err)
	}
}

func TestFullBackupDownloadReminderSurvivesRetentionAndRestart(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.FullBackups.Now = func() time.Time { return first }
	if _, err := c.FullBackups.Create(ctx, 0, "backup", func(w io.Writer) error { _, err := w.Write([]byte("archive")); return err }); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := c.DB.R.QueryRow(`SELECT count(*) FROM events WHERE type='full_backup_download_reminder'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	c.remindFullBackup(ctx, first.Add(6*24*time.Hour))
	if count() != 0 {
		t.Fatal("early reminder")
	}
	// New archives replace the first one before its seventh day.
	c.FullBackups.Now = func() time.Time { return first.Add(6 * 24 * time.Hour) }
	if _, err := c.FullBackups.Create(ctx, 0, "new backup", func(w io.Writer) error { _, err := w.Write([]byte("archive")); return err }); err != nil {
		t.Fatal(err)
	}
	if err := backup.Retain(c.FullBackups, 1); err != nil {
		t.Fatal(err)
	}
	c.remindFullBackup(ctx, first.Add(7*24*time.Hour))
	c.remindFullBackup(ctx, first.Add(7*24*time.Hour+time.Minute))
	if count() != 1 {
		t.Fatal("reminder missing or repeated")
	}
	download := first.Add(8 * 24 * time.Hour)
	if err := c.putBackupValue(ctx, fullDownloadKey, download.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	c.remindFullBackup(ctx, first.Add(14*24*time.Hour))
	if count() != 1 {
		t.Fatal("download did not reset reminder")
	}
	c.remindFullBackup(ctx, download.Add(7*24*time.Hour))
	if count() != 2 {
		t.Fatal("overdue download reminder missing")
	}
}
