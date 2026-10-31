package secrets

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "core.db"), migrations.Core())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.W.Exec(`INSERT INTO bots (id, name, port, created_at) VALUES (1, 'a', 20001, 'x'), (2, 'b', 20002, 'x')`)
	key, err := LoadMasterKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	box, _ := NewBox(key)
	return &Store{DB: db, Box: box, Redact: NewRedactor()}
}

func TestEncryptDecrypt(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Set(ctx, 1, TokenName, KindValue, []byte("123:secret-token")); err != nil {
		t.Fatal(err)
	}
	v, err := s.Decrypt(ctx, 1, TokenName)
	if err != nil || string(v) != "123:secret-token" {
		t.Fatalf("%q, %v", v, err)
	}
	var ct []byte
	s.DB.R.QueryRow(`SELECT ciphertext FROM secrets WHERE bot_id = 1`).Scan(&ct)
	if bytes.Contains(ct, []byte("secret-token")) {
		t.Fatal("stored in plain text")
	}
	list, _ := s.List(ctx, 1)
	if len(list) != 1 || list[0].Name != TokenName {
		t.Fatalf("list = %+v", list)
	}
}

func TestForeignAADFails(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	s.Set(ctx, 1, "PAYMENT_KEY", KindValue, []byte("pay-secret"))
	// Move the ciphertext to another bot and another name.
	s.DB.W.Exec(`INSERT INTO secrets (bot_id, name, kind, ciphertext, nonce, updated_at)
		SELECT 2, 'PAYMENT_KEY', kind, ciphertext, nonce, updated_at FROM secrets WHERE bot_id = 1`)
	s.DB.W.Exec(`INSERT INTO secrets (bot_id, name, kind, ciphertext, nonce, updated_at)
		SELECT 1, 'OTHER', kind, ciphertext, nonce, updated_at FROM secrets WHERE bot_id = 1 AND name = 'PAYMENT_KEY'`)
	if _, err := s.Decrypt(ctx, 2, "PAYMENT_KEY"); err == nil {
		t.Fatal("ciphertext moved to another bot decrypted")
	}
	if _, err := s.Decrypt(ctx, 1, "OTHER"); err == nil {
		t.Fatal("ciphertext moved to another name decrypted")
	}
}

func TestWrongMasterKey(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	s.Set(ctx, 1, "X_KEY", KindValue, []byte("value"))
	other := make([]byte, KeySize)
	s.Box.SetKey(other)
	if _, err := s.Decrypt(ctx, 1, "X_KEY"); err == nil {
		t.Fatal("decrypted with another key")
	}
}

func TestMasterKeyFile(t *testing.T) {
	dir := t.TempDir()
	k1, err := LoadMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := LoadMasterKey(dir)
	if !bytes.Equal(k1, k2) {
		t.Fatal("key changed")
	}
	if _, err := LoadMasterKey(filepath.Join(dir, "missing")); !errors.Is(err, ErrNoKeysDir) {
		t.Fatalf("missing /keys: %v", err)
	}
	b, _ := os.ReadFile(MasterKeyPath(dir))
	k3, err := ParseMasterKey(b)
	if err != nil || !bytes.Equal(k3, k1) {
		t.Fatal("export format does not round-trip")
	}
}

func TestNames(t *testing.T) {
	for _, n := range []string{"BOT_TOKEN", "_X", "a1"} {
		if !ValidName(n) {
			t.Errorf("%s rejected", n)
		}
	}
	for _, n := range []string{"", "1A", "A-B", "A B", "Я"} {
		if ValidName(n) {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestRedactor(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	s.Set(ctx, 1, TokenName, KindValue, []byte("123456:AAE-token"))
	s.Decrypt(ctx, 1, TokenName)
	var buf bytes.Buffer
	log := slog.New(s.Redact.Handler(slog.NewTextHandler(&buf, nil)))
	log.With("url", "https://api.telegram.org/bot123456:AAE-token/getMe").
		Error("call failed: 123456:AAE-token", "err", errors.New("Post bot123456:AAE-token: timeout"), slog.Group("g", "v", "123456:AAE-token"))
	if strings.Contains(buf.String(), "AAE-token") {
		t.Fatalf("token in log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "***") {
		t.Fatal("nothing redacted")
	}
}

func TestWriteFiles(t *testing.T) {
	dir := t.TempDir()
	paths, err := WriteFiles(dir, -1, []Secret{{Name: "CREDS", Kind: KindFile, Value: []byte(`{"k":1}`)}, {Name: "V", Kind: KindValue, Value: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("paths = %v", paths)
	}
	b, _ := os.ReadFile(paths["CREDS"])
	if string(b) != `{"k":1}` {
		t.Fatal("content")
	}
	// A second start replaces the read-only file.
	if _, err := WriteFiles(dir, -1, []Secret{{Name: "CREDS", Kind: KindFile, Value: []byte("new")}}); err != nil {
		t.Fatal(err)
	}
}
