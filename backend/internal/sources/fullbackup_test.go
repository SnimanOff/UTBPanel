package sources

import (
	"archive/tar"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func TestPanelBackupSnapshotAndOwnerBoundary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	writer := store.NewWriter(db, time.Hour, nil)
	defer func() { writer.Close(); db.Close() }()
	caDir := filepath.Join(dir, "pki")
	if _, err := pki.Open(ctx, caDir, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.W.Exec(`INSERT INTO settings(key,value) VALUES('test','from WAL')`); err != nil {
		t.Fatal(err)
	}
	extras := &backup.Files{Dir: filepath.Join(dir, "extras")}
	service := &FullBackups{DB: db, W: writer, PKI: caDir, Extras: extras}
	part, err := service.prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f, err := extras.Open(backup.FileRef{ID: part.File.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader := tar.NewReader(f)
	copyDir := t.TempDir()
	found := map[string]bool{}
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		found[h.Name] = true
		if h.Name == "panel.db" {
			file, err := os.Create(filepath.Join(copyDir, "panel.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(file, reader); err != nil {
				t.Fatal(err)
			}
			file.Close()
		}
	}
	if !found["pki/ca.key"] || !found["pki/ca.crt"] {
		t.Fatal("CA not backed up")
	}
	copy, err := dbgate.OpenReadOnly(filepath.Join(copyDir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var value string
	if err := copy.QueryRow(`SELECT value FROM settings WHERE key='test'`).Scan(&value); err != nil || value != "from WAL" {
		t.Fatal(value, err)
	}
	mux := api.NewMux()
	service.Register(mux, wire.NewMux())
	for _, method := range []string{"GET", "POST"} {
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, httptest.NewRequest(method, "/api/backups", nil))
		if out.Code != 403 {
			t.Fatal("non-owner backup access", out.Code)
		}
	}
}
