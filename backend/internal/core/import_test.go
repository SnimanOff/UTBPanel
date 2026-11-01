package core

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
)

func importFixture(t *testing.T, c *Core, badRows bool) string {
	t.Helper()
	d := botconfig.Document{Secrets: []string{"KEY"}, Env: map[string]string{"MODE": "restored"}, Flags: map[string]botconfig.FlagValue{"new_checkout": {Kind: "bool", Bool: true}}}
	botconfig.Normalize(&d)
	yaml, _ := botconfig.YAML(d)
	users := `{"version":1,"users":[{"bot_id":999,"user_id":42,"first_seen":"2026-01-01","last_seen":"2026-01-02","source":"promo","banned":1,"ban_reason":"test","username":"buyer","language_code":"ru","is_premium":0,"profile_updated_at":"2026-01-02"}],"allow":[{"user_id":42,"note":"owner"}]}`
	if badRows {
		users = `{"version":1,"users":[{"unexpected":"bad"}],"allow":[]}`
	}
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE orders(n);INSERT INTO orders VALUES(42)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var encrypted bytes.Buffer
	bin := linuxBot(t)
	err = backup.Encrypt(context.Background(), &encrypted, "import fixture password", backup.Manifest{Name: "source", TelegramID: 123, CurrentVersion: "v1", Releases: []deploy.Release{{Version: "v1"}}}, func(a *backup.Archive) error {
		if err := a.Bytes("bin/v1/bot", bin); err != nil {
			return err
		}
		for name, raw := range map[string]string{"config.yaml": yaml, "users.json": users, "payments.json": `{"version":1,"payments":[{"id":999,"bot_id":999,"ts":"2026-01-01","user_id":42,"currency":"XTR","amount":10,"charge_id":"old-charge"}]}`, "files/data.txt": "restored file"} {
			if err := a.Bytes(name, []byte(raw)); err != nil {
				return err
			}
		}
		return a.RootFile(root, "bot.db", "bot.db", false)
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Imports.Begin()
	if err != nil {
		t.Fatal(err)
	}
	raw := encrypted.Bytes()
	for offset := 0; offset < len(raw); offset += backup.ChunkSize {
		if err := c.Imports.Append(id, int64(offset), raw[offset:min(offset+backup.ChunkSize, len(raw))]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Imports.Inspect(context.Background(), id, "import fixture password"); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestImportRestoresNewBotAndConsumesFlagsOnce(t *testing.T) {
	ctx := context.Background()
	c := configCore(t)
	webhookCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "getMe" {
			io.WriteString(w, `{"ok":true,"result":{"id":123,"is_bot":true,"username":"imported"}}`)
		} else {
			webhookCalls++
			io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	defer srv.Close()
	c.TG.Base = srv.URL
	id := importFixture(t, c, false)
	request := backup.ImportApply{ID: id, Name: "restored", Token: "123:new-token", Secrets: map[string]backup.SecretInput{"KEY": {Kind: secrets.KindFile, Value: []byte{0, 1, 2}}}}
	b, err := c.ImportBot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID == 1 || b.State != StateStopped || b.Version != "v1" || webhookCalls != 0 {
		t.Fatal(b, webhookCalls)
	}
	if token, err := c.Token(ctx, b.ID); err != nil || token != "123:new-token" {
		t.Fatal(token, err)
	}
	key, err := c.Sec.Decrypt(ctx, b.ID, "KEY")
	if err != nil || !bytes.Equal(key, []byte{0, 1, 2}) {
		t.Fatal(key, err)
	}
	var source string
	if err := c.DB.R.QueryRow(`SELECT source FROM bot_users WHERE bot_id=? AND user_id=42`, b.ID).Scan(&source); err != nil || source != "promo" || !c.Bans.Banned(b.ID, 42) {
		t.Fatal(source, err)
	}
	var payments int
	c.DB.R.QueryRow(`SELECT COUNT(*) FROM payments WHERE bot_id=? AND charge_id='old-charge'`, b.ID).Scan(&payments)
	if payments != 1 {
		t.Fatal("payments missing")
	}
	if raw, err := os.ReadFile(filepath.Join(c.Cfg.DataDir, "bots", "2", "files", "data.txt")); err != nil || string(raw) != "restored file" {
		t.Fatal(string(raw), err)
	}
	defs := []flags.Definition{{Name: "new_checkout", Kind: "bool", Default: flags.Value{Kind: "bool"}}}
	state, err := c.SDKFlags.RegisterDefinitions(ctx, b.ID, "main", nil, defs)
	if err != nil || !state["new_checkout"].Value.Bool {
		t.Fatal(state, err)
	}
	if err := c.SDKFlags.Set(ctx, b.ID, "new_checkout", flags.Value{Kind: "bool"}, false); err != nil {
		t.Fatal(err)
	}
	state, err = c.SDKFlags.RegisterDefinitions(ctx, b.ID, "main", nil, defs)
	if err != nil || state["new_checkout"].Value.Bool {
		t.Fatal("import override was replayed", state, err)
	}
	again, err := c.ImportBot(ctx, request)
	if err != nil || again.ID != b.ID {
		t.Fatal("retry duplicated bot", again, err)
	}
	if webhookCalls != 0 {
		t.Fatal("import touched original webhook")
	}
}

func TestImportFailureCleansOnlyNewLocalData(t *testing.T) {
	ctx := context.Background()
	c := configCore(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) != "getMe" {
			calls++
		}
		io.WriteString(w, `{"ok":true,"result":{"id":123,"is_bot":true,"username":"imported"}}`)
	}))
	defer srv.Close()
	c.TG.Base = srv.URL
	id := importFixture(t, c, true)
	if _, err := c.ImportBot(ctx, backup.ImportApply{ID: id, Name: "broken", Token: "123:token", Secrets: map[string]backup.SecretInput{"KEY": {Kind: secrets.KindValue, Value: []byte("new")}}}); err == nil {
		t.Fatal("invalid rows accepted")
	}
	bots, err := c.Bots(ctx)
	if err != nil || len(bots) != 1 || bots[0].ID != 1 {
		t.Fatal("rollback removed existing bot or retained new bot", bots, err)
	}
	if _, err := os.Stat(filepath.Join(c.Cfg.DataDir, "bots", "2")); !os.IsNotExist(err) {
		t.Fatal("partial files retained", err)
	}
	if calls != 0 {
		t.Fatal("rollback changed Telegram webhook")
	}
}
