package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

func TestEncryptedArchive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("local data"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("must never escape the root"), 0o600)
	os.Symlink(outside, filepath.Join(dir, "escape"))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var encrypted bytes.Buffer
	payload := bytes.Repeat([]byte("record\n"), 100_000)
	if err := Encrypt(ctx, &encrypted, "six-random-words-are-not-stored", Manifest{Name: "shop"}, func(a *Archive) error {
		if err := a.Bytes("users.json", payload); err != nil {
			return err
		}
		return a.Tree(root, ".", "files")
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(encrypted.Bytes(), []byte("age-encryption.org/v1\n")) || bytes.Contains(encrypted.Bytes(), []byte("local data")) {
		t.Fatal("unencrypted archive")
	}
	wrong, _ := age.NewScryptIdentity("wrong-password")
	wrong.SetMaxWorkFactor(20)
	if _, err := age.Decrypt(bytes.NewReader(encrypted.Bytes()), wrong); err == nil {
		t.Fatal("wrong password accepted")
	}
	identity, _ := age.NewScryptIdentity("six-random-words-are-not-stored")
	reader, err := age.Decrypt(bytes.NewReader(encrypted.Bytes()), identity)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg {
			t.Fatal("special entry")
		}
		raw, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = raw
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal("age authentication", err)
	}
	if !bytes.Equal(files["users.json"], payload) || string(files["files/file.txt"]) != "local data" || files["files/escape"] != nil {
		t.Fatal("data lost or root escaped", files)
	}
	var manifest Manifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Format != "utbpanel.bot" || manifest.Version != 1 || len(manifest.Entries) != 2 {
		t.Fatalf("%+v", manifest)
	}
	for name, entry := range manifest.Entries {
		hash := sha256.Sum256(files[name])
		if entry.SHA256 != hex.EncodeToString(hash[:]) || entry.Size != int64(len(files[name])) {
			t.Fatal("bad entry digest", name)
		}
	}
}

func TestArchiveRefusesUnsafeNames(t *testing.T) {
	for _, name := range []string{"../outside", "a/../outside", "/absolute", "a\\b", "manifest.json"} {
		var buf bytes.Buffer
		a := &Archive{ctx: context.Background(), tar: tar.NewWriter(&buf), manifest: &Manifest{Entries: map[string]Entry{}}}
		if err := a.Bytes(name, []byte("x")); err == nil {
			t.Error("accepted", name)
		}
	}
	var buf bytes.Buffer
	a := &Archive{ctx: context.Background(), tar: tar.NewWriter(&buf), manifest: &Manifest{Entries: map[string]Entry{}}}
	if err := a.Add("cut", 10, strings.NewReader("short")); err == nil {
		t.Fatal("short file accepted")
	}
}

func TestPasswordAndExpiringFiles(t *testing.T) {
	password, err := Password()
	if err != nil || len(strings.Fields(password)) != 6 {
		t.Fatal(password, err)
	}
	now := time.Now()
	store := &Files{Dir: filepath.Join(t.TempDir(), "exports"), TTL: time.Minute, Now: func() time.Time { return now }}
	data := bytes.Repeat([]byte("abc"), ChunkSize)
	file, err := store.Create(context.Background(), 7, Filename("../../магазин", 7), func(w io.Writer) error { _, err := w.Write(data); return err })
	if err != nil {
		t.Fatal(err)
	}
	ref := FileRef{ID: file.ID, BotID: 7}
	reader := &RemoteReader{Ctx: context.Background(), Ref: ref, Size: file.Size, ReadChunk: func(_ context.Context, ref FileRef) ([]byte, error) { return store.Read(ref) }}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("chunked read", err)
	}
	if _, err := store.Info(FileRef{ID: file.ID, BotID: 8}); !errors.Is(err, ErrExpired) {
		t.Fatal("cross-bot file read", err)
	}
	if _, err := store.Read(FileRef{ID: "../../outside", BotID: 7}); !errors.Is(err, ErrExpired) {
		t.Fatal("path traversal", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := store.Info(ref); !errors.Is(err, ErrExpired) {
		t.Fatal("expired file available", err)
	}
	store.Prune()
	entries, _ := os.ReadDir(store.Dir)
	if len(entries) != 0 {
		t.Fatal("expired data retained", entries)
	}
	if _, err := store.Create(context.Background(), 7, "failed", func(w io.Writer) error { w.Write([]byte("partial")); return errors.New("failed") }); err == nil {
		t.Fatal("partial archive accepted")
	}
	entries, _ = os.ReadDir(store.Dir)
	if len(entries) != 0 {
		t.Fatal("partial archive retained")
	}
}
