package panel

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
)

func coreConfig(t *testing.T, dir, run string) config.Core {
	t.Helper()
	cfg := config.DefaultCore()
	cfg.DataDir = filepath.Join(dir, "core")
	cfg.KeysDir = filepath.Join(dir, "keys")
	cfg.RunDir = run
	cfg.HostProc = "/proc"
	os.MkdirAll(cfg.KeysDir, 0o700)
	return cfg
}
func panelConfig(dir, run string) config.Panel {
	cfg := config.DefaultPanel()
	cfg.DataDir = filepath.Join(dir, "panel")
	cfg.RunDir = run
	cfg.Listen = "127.0.0.1:0"
	cfg.PublicAddr = "127.0.0.1"
	return cfg
}

func TestRestoreReopensCorePanelAndPreservesDeviceKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sourceRun, err := os.MkdirTemp("", "restore-src-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sourceRun)
	sourceDir := t.TempDir()
	sourceCore, err := core.New(ctx, coreConfig(t, sourceDir, sourceRun), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sourceCore.Logs.Close(); sourceCore.W.Close(); sourceCore.DB.Close() }()
	sourcePanel, err := New(ctx, panelConfig(sourceDir, sourceRun), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sourcePanel.W.Close(); sourcePanel.DB.Close() }()
	if _, err := sourceCore.DB.W.Exec(`INSERT INTO bots(id,name,port,state,created_at) VALUES(7,'restored',20000,'stopped','x')`); err != nil {
		t.Fatal(err)
	}
	if err := sourceCore.Sec.Set(ctx, 7, "KEY", secrets.KindValue, []byte("restored secret")); err != nil {
		t.Fatal(err)
	}
	deviceKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	deviceCert, err := sourcePanel.CA.Issue(ctx, pki.Owner, "existing owner", "old-device", deviceKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	for name, src := range map[string]string{"core.db": sourceCore.DB.Path, "panel.db": sourcePanel.DB.Path} {
		if err := dbgate.Snapshot(ctx, src, filepath.Join(tmp, name), -1); err != nil {
			t.Fatal(err)
		}
	}
	root, _ := os.OpenRoot(tmp)
	defer root.Close()
	pkiRoot, _ := os.OpenRoot(filepath.Join(sourcePanel.Cfg.DataDir, "pki"))
	defer pkiRoot.Close()
	keyText, _ := os.ReadFile(secrets.MasterKeyPath(sourceCore.Cfg.KeysDir))
	key, err := secrets.ParseMasterKey(keyText)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := backup.EncryptFull(ctx, &encrypted, key, backup.FullManifest{Bots: []backup.FullBot{{ID: 7, Name: "restored", State: "stopped"}}}, func(a *backup.Archive) error {
		if err := a.RootFile(root, "core.db", "core.db", false); err != nil {
			return err
		}
		if err := a.RootFile(root, "panel.db", "panel.db", false); err != nil {
			return err
		}
		if err := a.Tree(pkiRoot, ".", "pki"); err != nil {
			return err
		}
		return a.Bytes("bots/7/files/file.txt", []byte("restored file"))
	}); err != nil {
		t.Fatal(err)
	}
	targetRun, err := os.MkdirTemp("", "restore-dst-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(targetRun)
	targetDir := t.TempDir()
	ccfg := coreConfig(t, targetDir, targetRun)
	pcfg := panelConfig(targetDir, targetRun)
	var mu sync.Mutex
	var latestCore *core.Core
	var latestPanel *Panel
	failures := make(chan error, 4)
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			instance, err := core.New(ctx, ccfg, slog.Default())
			if err != nil {
				failures <- err
				return
			}
			mu.Lock()
			latestCore = instance
			mu.Unlock()
			err = instance.Run(ctx)
			if errors.Is(err, app.ErrRestart) && ctx.Err() == nil {
				continue
			}
			if err != nil {
				failures <- err
			}
			return
		}
	})
	workers.Go(func() {
		for {
			instance, err := New(ctx, pcfg, slog.Default())
			if err != nil {
				failures <- err
				return
			}
			mu.Lock()
			latestPanel = instance
			mu.Unlock()
			err = instance.Run(ctx)
			if errors.Is(err, app.ErrRestart) && ctx.Err() == nil {
				continue
			}
			if err != nil {
				failures <- err
			}
			return
		}
	})
	defer func() {
		cancel()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("restore services did not shut down")
		}
	}()
	var cli interface {
		Call(context.Context, string, any, any) error
		Close() error
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for cli == nil {
		select {
		case err := <-failures:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
			connection, err := core.DialCLI(ctx, ccfg)
			if err == nil {
				cli = connection
			}
		}
	}
	defer cli.Close()
	for {
		mu.Lock()
		panel := latestPanel
		mu.Unlock()
		if panel != nil && panel.Core.Conn() != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	var id string
	mu.Lock()
	panelPeer := latestPanel.Core
	mu.Unlock()
	if err := panelPeer.Call(ctx, "restore.begin", struct{}{}, &id); err == nil {
		t.Fatal("panel peer can invoke the SSH restore command")
	}
	if err := cli.Call(ctx, "restore.begin", struct{}{}, &id); err != nil {
		t.Fatal(err)
	}
	raw := encrypted.Bytes()
	for offset := 0; offset < len(raw); offset += backup.ChunkSize {
		if err := cli.Call(ctx, "restore.chunk", backup.ImportChunk{ID: id, Offset: int64(offset), Data: raw[offset:min(offset+backup.ChunkSize, len(raw))]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var result backup.RestoreResult
	if err := cli.Call(ctx, "restore.prepare", id, &result); err == nil {
		t.Fatal("wrong master key accepted")
	}
	if err := cli.Call(ctx, "masterkey.import", key, nil); err != nil {
		t.Fatal(err)
	}
	if err := cli.Call(ctx, "restore.prepare", id, &result); err != nil {
		t.Fatal(err)
	}
	if result.Bots != 1 {
		t.Fatal(result)
	}
	mu.Lock()
	oldCore, oldPanel := latestCore, latestPanel
	mu.Unlock()
	if err := cli.Call(ctx, "restore.commit", id, &result); err != nil {
		t.Fatal(err)
	}
	for {
		mu.Lock()
		currentCore, currentPanel := latestCore, latestPanel
		mu.Unlock()
		if currentCore != nil && currentPanel != nil && currentCore != oldCore && currentPanel != oldPanel {
			value, err := currentCore.Sec.Decrypt(ctx, 7, "KEY")
			if err != nil || string(value) != "restored secret" {
				t.Fatal(string(value), err)
			}
			if !bytes.Equal(currentPanel.CA.Fingerprint(), sourcePanel.CA.Fingerprint()) {
				t.Fatal("CA changed")
			}
			if _, err := currentPanel.CA.Verify(deviceCert); err != nil {
				t.Fatal("existing device key stopped working", err)
			}
			if raw, err := os.ReadFile(filepath.Join(ccfg.DataDir, "bots", "7", "files", "file.txt")); err != nil || string(raw) != "restored file" {
				t.Fatal(string(raw), err)
			}
			connection, err := core.DialCLI(ctx, ccfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := connection.Call(ctx, "restore.begin", struct{}{}, &id); err == nil {
				t.Fatal("restore accepted into a populated installation")
			}
			connection.Close()
			break
		}
		select {
		case err := <-failures:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("restore did not finish", ctx.Err())
		case <-ticker.C:
		}
	}
}
