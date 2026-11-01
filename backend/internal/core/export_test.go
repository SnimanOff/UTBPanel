package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
)

func TestBotExportContainsOnlyItsPortableData(t *testing.T) {
	ctx := context.Background()
	c := configCore(t)
	// Root must be able to switch to the bot UID during the SQLite snapshot.
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		dir, err := os.MkdirTemp("", "core-export-data-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		os.Chmod(dir, 0o711)
		c.Cfg.DataDir = dir
		c.Exports.Dir = filepath.Join(dir, "exports")
	}
	dir := filepath.Join(c.Cfg.DataDir, "bots", "1")
	os.MkdirAll(filepath.Join(dir, "files"), 0o750)
	os.WriteFile(filepath.Join(dir, "files", "data.txt"), []byte("bot data"), 0o600)
	os.MkdirAll(filepath.Join(dir, "secrets"), 0o700)
	os.WriteFile(filepath.Join(dir, "secrets", "FILE_KEY"), []byte("private-file-secret"), 0o400)
	for _, version := range []string{"v1", "v2"} {
		releaseDir := filepath.Join(dir, "releases", version)
		os.MkdirAll(releaseDir, 0o750)
		os.WriteFile(filepath.Join(releaseDir, "bot"), []byte("binary-"+version), 0o500)
		meta, _ := json.Marshal(deploy.Release{Version: version})
		os.WriteFile(filepath.Join(releaseDir, "release.json"), meta, 0o600)
	}
	c.Releases.Dir = func(int64) string { return filepath.Join(dir, "releases") }
	c.DBG.Path = func(int64) string { return filepath.Join(dir, "bot.db") }
	c.DB.W.Exec(`UPDATE bots SET current_version='v2' WHERE id=1`)
	for name, value := range map[string]string{secrets.TokenName: "111:private-token", "FILE_KEY": "private-file-secret"} {
		if err := c.Sec.Set(ctx, 1, name, secrets.KindValue, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	c.DB.W.Exec(`INSERT INTO bot_users(bot_id,user_id,first_seen,last_seen,source,banned,ban_reason) VALUES(1,42,'2026-01-01','2026-01-02','promo',1,'test'),(2,43,'2026-01-01','2026-01-02','other-bot',0,'')`)
	c.DB.W.Exec(`INSERT INTO bot_allow(bot_id,user_id,note) VALUES(1,42,'owner')`)
	c.DB.W.Exec(`INSERT INTO payments(bot_id,ts,user_id,currency,amount,charge_id) VALUES(1,'2026-01-01',42,'XTR',10,'charge-1'),(2,'2026-01-01',43,'XTR',20,'charge-2')`)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE orders(id); INSERT INTO orders VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		uid := runner.UID(1)
		os.Chmod(filepath.Join(c.Cfg.DataDir, "bots"), 0o711)
		os.Chmod(dir, 0o750)
		os.Chown(dir, uid, uid)
		os.Chown(filepath.Join(dir, "bot.db"), uid, uid)
	}
	snapshot, err := c.ConfigSnapshot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	file, err := c.ExportBot(ctx, backup.ExportRequest{BotID: 1, Config: snapshot, Password: "correct export passphrase", Options: backup.Options{Files: true, AllVersions: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := backup.FileRef{ID: file.ID, BotID: 1}
	var encrypted bytes.Buffer
	for ref.Offset < file.Size {
		raw, err := c.Exports.Read(ref)
		if err != nil {
			t.Fatal(err)
		}
		encrypted.Write(raw)
		ref.Offset += int64(len(raw))
	}
	identity, _ := age.NewScryptIdentity("correct export passphrase")
	plain, err := age.Decrypt(bytes.NewReader(encrypted.Bytes()), identity)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		h, err := tarReader.Next()
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
		files[h.Name] = raw
	}
	if len(files["bot.db"]) == 0 || string(files["files/data.txt"]) != "bot data" || string(files["bin/v2/bot"]) != "binary-v2" || string(files["bin/v1/bot"]) != "binary-v1" {
		t.Fatal("archive is incomplete")
	}
	for name, raw := range files {
		if strings.Contains(name, "secrets/") || bytes.Contains(raw, []byte("111:private-token")) || bytes.Contains(raw, []byte("private-file-secret")) || bytes.Contains(raw, []byte("other-bot")) || bytes.Contains(raw, []byte("charge-2")) {
			t.Fatalf("private or cross-bot data leaked in %s", name)
		}
	}
	if !bytes.Contains(files["users.json"], []byte("promo")) || !bytes.Contains(files["payments.json"], []byte("charge-1")) {
		t.Fatal("user data missing")
	}
	if _, err := c.ExportBot(ctx, backup.ExportRequest{BotID: 1, Config: snapshot, Password: "too short"}, nil); err == nil {
		t.Fatal("short password accepted")
	}
	if err := c.SetSetting(ctx, 1, "tz", "UTC"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExportBot(ctx, backup.ExportRequest{BotID: 1, Config: snapshot, Password: "correct export passphrase"}, nil); !conflict(err) {
		t.Fatal("stale export accepted", err)
	}
}
