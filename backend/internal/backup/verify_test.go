package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func TestVerificationSkipsBeforeReadingWithInsufficientDisk(t *testing.T) {
	ctx := context.Background()
	files := FullFiles(t.TempDir())
	file, err := files.Create(ctx, 0, "broken", func(w io.Writer) error { _, err := w.Write([]byte("invalid archive")); return err })
	if err != nil {
		t.Fatal(err)
	}
	verifier := Verifier{Dir: filepath.Join(t.TempDir(), "tmp"), FreeSpace: func(string) (uint64, error) { return uint64(file.Size)*2 - 1, nil }}
	result := verifier.Verify(ctx, files, file, make([]byte, 32))
	if result.State != "skipped" || result.Error == "" {
		t.Fatal(result)
	}
	verifier.FreeSpace = func(string) (uint64, error) { return uint64(file.Size) * 2, nil }
	result = verifier.Verify(ctx, files, file, make([]byte, 32))
	if result.State != "failed" || result.Error == "" {
		t.Fatal("invalid archive accepted", result)
	}
	entries, err := os.ReadDir(verifier.Dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("plaintext verification directory leaked", entries, err)
	}
	updated, err := files.MarkVerified(FileRef{ID: file.ID}, result)
	if err != nil || updated.Verification == nil || updated.Verification.State != "failed" {
		t.Fatal(updated, err)
	}
	if err := files.MarkDownloaded(FileRef{ID: file.ID}); err != nil {
		t.Fatal(err)
	}
	info, err := files.Info(FileRef{ID: file.ID})
	if err != nil || info.Verification == nil || info.Verification.State != "failed" || info.DownloadedAt.IsZero() {
		t.Fatal("download overwrote verification", info, err)
	}
}

func TestVerificationChecksDatabasesBinariesAndHashes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	core, err := store.Open(ctx, filepath.Join(dir, "core-live.db"), migrations.Core())
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	panel, err := store.Open(ctx, filepath.Join(dir, "panel-live.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer panel.Close()
	if _, err := pki.Open(ctx, filepath.Join(dir, "pki"), panel); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		db   *store.DB
		name string
	}{{core, "core.db"}, {panel, "panel.db"}} {
		if err := dbgate.Snapshot(ctx, item.db.Path, filepath.Join(dir, item.name), -1); err != nil {
			t.Fatal(err)
		}
	}
	base := map[string][]byte{}
	for _, name := range []string{"core.db", "panel.db", "pki/ca.crt", "pki/ca.key"} {
		base[name], err = os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"valid", "core.db", "panel.db", "bots/1/bot.db", "ELF", "checksum", "tamper"} {
		t.Run(mode, func(t *testing.T) {
			files := FullFiles(t.TempDir())
			key := bytes.Repeat([]byte{4}, 32)
			file, err := files.Create(ctx, 0, "test.backup", func(w io.Writer) error {
				cipher, err := NewChunkWriter(w, key)
				if err != nil {
					return err
				}
				err = writeArchive(ctx, cipher, func(a *Archive) error {
					for name, data := range base {
						if name == mode {
							data = []byte("broken database")
						}
						if err := a.Bytes(name, data); err != nil {
							return err
						}
					}
					if mode == "bots/1/bot.db" {
						return a.Bytes(mode, []byte("broken database"))
					}
					if mode == "ELF" {
						return a.Bytes("bots/1/releases/v1/bot", []byte("MZ not a Linux binary"))
					}
					return nil
				}, func(entries map[string]Entry) ([]byte, error) {
					if mode == "checksum" {
						entry := entries["core.db"]
						entry.SHA256 = strings.Repeat("0", 64)
						entries["core.db"] = entry
					}
					return json.Marshal(FullManifest{Format: "utbpanel.full", Version: FormatVersion, Entries: entries})
				})
				if err != nil {
					return err
				}
				return cipher.Close()
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "tamper" {
				name := filepath.Join(files.Dir, file.ID)
				raw, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				raw[len(raw)/2] ^= 1
				if err := os.WriteFile(name, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			v := Verifier{Dir: t.TempDir(), FreeSpace: func(string) (uint64, error) { return 1 << 60, nil }}
			result := v.Verify(ctx, files, file, key)
			if mode == "valid" {
				if result.State != "passed" {
					t.Fatal(result)
				}
			} else if result.State != "failed" || result.Error == "" {
				t.Fatal("invalid archive accepted", result)
			}
			if mode == "ELF" && !strings.Contains(result.Error, "not an ELF") {
				t.Fatal(result)
			}
			if strings.HasSuffix(mode, ".db") && !strings.Contains(result.Error, mode) {
				t.Fatal(result)
			}
			if mode == "checksum" && !strings.Contains(result.Error, "checksum mismatch") {
				t.Fatal(result)
			}
			if entries, err := os.ReadDir(v.Dir); err != nil || len(entries) != 0 {
				t.Fatal("verification data leaked", entries, err)
			}
		})
	}
}
