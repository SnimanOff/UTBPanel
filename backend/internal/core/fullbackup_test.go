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
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
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
	if err := backup.Parts(ctx, &panelPart, func(a *backup.Archive) error {
		for name, data := range map[string]string{"panel.db": "panel snapshot", "pki/ca.key": "private CA key", "pki/ca.crt": "CA cert"} {
			if err := a.Bytes(name, []byte(data)); err != nil {
				return err
			}
		}
		return nil
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
	if len(entries["core.db"]) == 0 || string(entries["pki/ca.key"]) != "private CA key" || string(entries["panel.db"]) != "panel snapshot" {
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
