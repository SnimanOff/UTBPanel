//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/panel"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/sources"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.yaml.in/yaml/v3"
)

// This phase is self-contained: real core/panel executables, separate volumes,
// real mTLS HTTP and the SSH CLI. It can also run without Docker:
// go test -tags=e2e -run '^TestBackupAndTransfer$' ./e2e
func TestBackupAndTransfer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			io.WriteString(w, `{"ok":true,"result":{"id":4242,"is_bot":true,"first_name":"Transfer","username":"transfer_bot"}}`)
		case "getMyCommands":
			io.WriteString(w, `{"ok":true,"result":[]}`)
		case "setWebhook", "deleteWebhook":
			io.WriteString(w, `{"ok":true,"result":true}`)
		default:
			io.WriteString(w, `{"ok":true,"result":{}}`)
		}
	}))
	defer tg.Close()
	coreBin := backupBuild(t, "core", "./cmd/core", false)
	panelBin := backupBuild(t, "panel", "./cmd/panel", false)
	botBin, err := os.ReadFile(backupBuild(t, "bot", "./internal/runner/testdata/testbot", true))
	if err != nil {
		t.Fatal(err)
	}
	source := newBackupInstallation(t, ctx, tg.URL, botBin)
	target := newBackupInstallation(t, ctx, tg.URL, nil)
	source.start(t, ctx, coreBin, panelBin)
	target.start(t, ctx, coreBin, panelBin)

	var exported sources.Exported
	source.json(t, "POST", "/api/exports/bots", map[string]any{"bot_ids": []int64{7}, "files": true, "metrics": true}, 201, &exported)
	if len(exported.Files) != 1 || exported.Password == "" {
		t.Fatal("no export returned")
	}
	portable := source.request(t, "GET", "/api/bots/7/exports/"+exported.Files[0].ID, nil, "", 200)
	if bytes.Contains(portable, []byte("source-secret")) || bytes.Contains(portable, []byte("4242:source")) {
		t.Fatal("portable archive exposed a secret")
	}
	target.inspect(t, portable, "incorrect transfer password", false)
	changed := bytes.Clone(portable)
	changed[len(changed)/2] ^= 1
	target.inspect(t, changed, exported.Password, false)
	var empty []core.Bot
	target.json(t, "GET", "/api/bots", nil, 200, &empty)
	if len(empty) != 0 {
		t.Fatal("failed imports created bots")
	}
	preview := target.inspect(t, portable, exported.Password, true)
	if len(preview.Document.Secrets) != 1 || preview.Document.Secrets[0] != "KEY" {
		t.Fatal("secret names were not preserved")
	}
	var imported sources.Imported
	target.json(t, "POST", "/api/imports/bots/"+preview.ID+"/apply", map[string]any{"name": "transferred", "token": "4242:target", "start": false, "secrets": map[string]any{"KEY": map[string]string{"kind": "value", "value": "replacement-secret"}}}, 201, &imported)
	if imported.Bot.ID == 7 || imported.Bot.ID == 0 || imported.Started || len(imported.Warnings) != 0 {
		t.Fatalf("unexpected import result: %+v", imported)
	}
	var document botconfig.View
	target.json(t, "GET", fmt.Sprintf("/api/bots/%d/config", imported.Bot.ID), nil, 200, &document)
	if document.Document.Env["MODE"] != "portable" {
		t.Fatal("configuration was not transferred", document)
	}
	checkBackupData(t, ctx, target.core.DataDir, imported.Bot.ID, "replacement-secret", target.master)
	checkMCPModes(t, ctx, source)

	var full backup.File
	source.json(t, "POST", "/api/backups", map[string]bool{"files": true}, 201, &full)
	if full.Verification == nil || full.Verification.State != "passed" {
		t.Fatal("full backup verification failed", full.Verification)
	}
	archive := source.request(t, "GET", "/api/backups/"+full.ID, nil, "", 200)
	clean := newBackupInstallation(t, ctx, tg.URL, nil)
	clean.start(t, ctx, coreBin, panelBin)
	clean.cli(t, ctx, coreBin, source.master, true, "masterkey", "import")
	broken := bytes.Clone(archive)
	broken[len(broken)/2] ^= 1
	clean.cli(t, ctx, coreBin, broken, false, "backup", "restore")
	clean.json(t, "GET", "/api/bots", nil, 200, &empty)
	if len(empty) != 0 {
		t.Fatal("damaged backup changed the clean installation")
	}
	clean.cli(t, ctx, coreBin, archive, true, "backup", "restore")
	// Use the original device certificate and CA against the new address.
	oldClient := source.client
	restored := *clean
	restored.client = oldClient
	restored.wait(t, ctx)
	var bots []core.Bot
	restored.json(t, "GET", "/api/bots", nil, 200, &bots)
	if len(bots) != 1 || bots[0].ID != 7 || bots[0].State != "stopped" {
		t.Fatal("saved bot state was lost", bots)
	}
	checkBackupData(t, ctx, clean.core.DataDir, 7, "source-secret", source.master)
	clean.client.CloseIdleConnections()
	if response, err := clean.client.Get(clean.url + "/api/ping"); err == nil {
		response.Body.Close()
		t.Fatal("discarded clean-installation CA is still accepted")
	}
}

type mcpDeviceTransport struct{ base http.RoundTripper }

func (tr mcpDeviceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	r.Header.Set("X-Panel", "1")
	r.Header.Set("X-Panel-Source", "mcp")
	return tr.base.RoundTrip(r)
}
func checkMCPModes(t *testing.T, ctx context.Context, installation *backupInstallation) {
	t.Helper()
	var group bots.Group
	installation.json(t, "POST", "/api/groups", map[string]string{"name": "MCP test group"}, 201, &group)
	installation.json(t, "PUT", "/api/bots/7/group", map[string]int64{"group_id": group.ID}, 200, nil)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "e2e", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: installation.url + "/mcp", HTTPClient: &http.Client{Transport: mcpDeviceTransport{installation.client.Transport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	invoke := func(name string, args any, wantError bool) *mcpsdk.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError {
			t.Fatalf("%s: %+v", name, result)
		}
		return result
	}
	args := map[string]any{"id": 7, "body": map[string]string{"name": "MCP renamed"}}
	invoke("get_bot", map[string]any{"id": 7}, false)
	overview := invoke("get_bot_overview", map[string]any{"id": 7}, false)
	if !strings.Contains(overview.Content[0].(*mcpsdk.TextContent).Text, `"webhook_live":true`) {
		t.Fatal("overview did not read the live webhook state")
	}
	invoke("rename_bot", args, true)
	setMode := func(mode string) {
		t.Helper()
		installation.json(t, "PUT", fmt.Sprintf("/api/mcp/groups/%d", group.ID), map[string]string{"mode": mode}, 204, nil)
	}
	setMode("full")
	invoke("rename_bot", args, false)
	setMode("read")
	invoke("rename_bot", args, true)
	setMode("off")
	invoke("get_bot", map[string]any{"id": 7}, true)
	list := invoke("list_bots", map[string]any{}, false)
	if strings.Contains(list.Content[0].(*mcpsdk.TextContent).Text, `"id":7`) {
		t.Fatal("disabled group leaked through the list")
	}
	setMode("read")
	var entries []audit.Entry
	installation.json(t, "GET", "/api/audit?source=mcp", nil, 200, &entries)
	if len(entries) < 6 {
		t.Fatal("MCP calls were not recorded by the real panel", entries)
	}
	for _, entry := range entries {
		if entry.Source != "mcp" || entry.Key != "transfer owner" {
			t.Fatal("MCP call lost device identity", entry)
		}
	}
}

func backupBuild(t *testing.T, name, pkg string, linux bool) string {
	t.Helper()
	if runtime.GOOS == "windows" && !linux {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-buildvcs=false", "-o", path, pkg)
	command.Dir = ".."
	command.Env = backupEnvironment()
	if linux {
		command.Env = append(command.Env, "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	}
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	return path
}
func backupEnvironment() []string {
	var out []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "TGPANEL_") {
			out = append(out, entry)
		}
	}
	return out
}

type backupInstallation struct {
	core                         config.Core
	panel                        config.Panel
	coreConfig, panelConfig, url string
	client                       *http.Client
	master                       []byte
}

func newBackupInstallation(t *testing.T, ctx context.Context, telegram string, bot []byte) *backupInstallation {
	t.Helper()
	dir, err := os.MkdirTemp("", "utb-transfer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o711); err != nil {
		t.Fatal(err)
	}
	listen, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listen.Addr().String()
	listen.Close()
	i := &backupInstallation{core: config.DefaultCore(), panel: config.DefaultPanel(), url: "https://" + addr}
	i.core.DataDir = filepath.Join(dir, "core")
	i.core.KeysDir = filepath.Join(dir, "keys")
	i.core.RunDir = filepath.Join(dir, "run")
	i.core.PublicAddr = "127.0.0.1"
	i.core.TelegramAPI = telegram
	i.core.HostProc = "/proc"
	i.core.LogLevel = "error"
	i.panel.DataDir = filepath.Join(dir, "panel")
	i.panel.RunDir = i.core.RunDir
	i.panel.Listen = addr
	i.panel.PublicAddr = "127.0.0.1"
	i.panel.LogLevel = "error"
	if err := os.MkdirAll(i.core.KeysDir, 0o700); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := core.New(ctx, i.core, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Logs.Close(); c.W.Close(); c.DB.Close() }()
	p, err := panel.New(ctx, i.panel, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { p.W.Close(); p.DB.Close() }()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := p.CA.Issue(ctx, pki.Owner, "transfer owner", "e2e", key.Public())
	if err != nil {
		t.Fatal(err)
	}
	i.client = &http.Client{Timeout: 45 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: p.CA.Pool(), Certificates: []tls.Certificate{{Certificate: [][]byte{cert.Raw}, PrivateKey: key, Leaf: cert}}}}}
	t.Cleanup(i.client.CloseIdleConnections)
	i.master, err = os.ReadFile(secrets.MasterKeyPath(i.core.KeysDir))
	if err != nil {
		t.Fatal(err)
	}
	if bot != nil {
		seedBackupBot(t, ctx, c, bot)
	}
	i.coreConfig = filepath.Join(dir, "core.yaml")
	i.panelConfig = filepath.Join(dir, "panel.yaml")
	for name, value := range map[string]any{i.coreConfig: i.core, i.panelConfig: i.panel} {
		raw, err := yaml.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return i
}

func seedBackupBot(t *testing.T, ctx context.Context, c *core.Core, bin []byte) {
	t.Helper()
	if _, err := c.DB.W.Exec(`INSERT INTO bots(id,name,username,tg_id,port,state,current_version,created_at,settings) VALUES(7,'source','transfer_bot',4242,21007,'stopped','v1','2026-01-01','{"profile_cache":{},"env":{"MODE":"portable"}}')`); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{secrets.TokenName: "4242:source", "KEY": "source-secret"} {
		if err := c.Sec.Set(ctx, 7, name, secrets.KindValue, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`INSERT INTO tg_users(user_id,username,language_code,is_premium,updated_at) VALUES(42,'buyer','ru',0,'2026-01-01')`,
		`INSERT INTO bot_users(bot_id,user_id,first_seen,last_seen,source,banned,ban_reason) VALUES(7,42,'2026-01-01','2026-01-02','promo',1,'e2e')`,
		`INSERT INTO payments(bot_id,ts,user_id,currency,amount,charge_id) VALUES(7,'2026-01-01',42,'XTR',17,'transfer-charge')`,
	} {
		if _, err := c.DB.W.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(c.Cfg.DataDir, "bots", "7")
	if err := os.MkdirAll(filepath.Join(dir, "releases", "v1"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o750); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(deploy.Release{Version: "v1"})
	for name, data := range map[string][]byte{"releases/v1/bot": bin, "releases/v1/release.json": meta, "files/data.txt": []byte("transferred data")} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE orders(id INTEGER);INSERT INTO orders VALUES(42)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		if err := os.Chmod(c.Cfg.DataDir, 0o711); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(dir), 0o711); err != nil {
			t.Fatal(err)
		}
		if err := filepath.WalkDir(dir, func(name string, _ os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Chown(name, runner.UID(7), runner.UID(7))
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func (i *backupInstallation) start(t *testing.T, ctx context.Context, coreBin, panelBin string) {
	t.Helper()
	for _, item := range []struct{ binary, config string }{{coreBin, i.coreConfig}, {panelBin, i.panelConfig}} {
		processCtx, stop := context.WithCancel(ctx)
		command := exec.CommandContext(processCtx, item.binary, "serve", "--config", item.config)
		command.Env = backupEnvironment()
		log, err := os.CreateTemp(filepath.Dir(i.coreConfig), "process-*.log")
		if err != nil {
			t.Fatal(err)
		}
		command.Stdout = log
		command.Stderr = log
		if err := command.Start(); err != nil {
			log.Close()
			stop()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		t.Cleanup(func() {
			stop()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("process did not exit")
			}
			log.Close()
			if t.Failed() {
				raw, _ := os.ReadFile(log.Name())
				t.Log(string(raw))
			}
		})
	}
	i.wait(t, ctx)
}
func (i *backupInstallation) wait(t *testing.T, ctx context.Context) {
	t.Helper()
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		r, err := http.NewRequestWithContext(wait, "GET", i.url+"/api/bots", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := i.client.Do(r)
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == 200 {
				return
			}
		}
		select {
		case <-wait.Done():
			t.Fatal("installation did not become ready", wait.Err(), err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func (i *backupInstallation) request(t *testing.T, method, path string, body io.Reader, contentType string, want int) []byte {
	t.Helper()
	r, err := http.NewRequest(method, i.url+path, body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Panel", "1")
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	response, err := i.client.Do(r)
	if err != nil {
		t.Fatal(method, path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s: %d, want %d: %s", method, path, response.StatusCode, want, raw)
	}
	return raw
}
func (i *backupInstallation) json(t *testing.T, method, path string, in any, want int, out any) {
	t.Helper()
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	raw := i.request(t, method, path, body, "application/json", want)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
}
func (i *backupInstallation) inspect(t *testing.T, archive []byte, password string, valid bool) backup.Preview {
	t.Helper()
	var data bytes.Buffer
	w := multipart.NewWriter(&data)
	if err := w.WriteField("password", password); err != nil {
		t.Fatal(err)
	}
	f, err := w.CreateFormFile("file", "bot.tgbot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := 200
	if !valid {
		want = 400
	}
	raw := i.request(t, "POST", "/api/imports/bots", &data, w.FormDataContentType(), want)
	var out backup.Preview
	if valid {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func (i *backupInstallation) cli(t *testing.T, ctx context.Context, binary string, input []byte, success bool, args ...string) {
	t.Helper()
	args = append(args, "--config", i.coreConfig)
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = backupEnvironment()
	command.Stdin = bytes.NewReader(input)
	out, err := command.CombinedOutput()
	if (err == nil) != success {
		t.Fatalf("CLI %s: %v\n%s", strings.Join(args[:2], " "), err, out)
	}
}
func checkBackupData(t *testing.T, ctx context.Context, dir string, id int64, secret string, keyText []byte) {
	t.Helper()
	db, err := dbgate.OpenReadOnly(filepath.Join(dir, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for query, want := range map[string]int{`SELECT count(*) FROM bot_users WHERE bot_id=? AND user_id=42 AND banned=1`: 1, `SELECT sum(amount) FROM payments WHERE bot_id=? AND user_id=42`: 17} {
		var got int
		if err := db.QueryRowContext(ctx, query, id).Scan(&got); err != nil || got != want {
			t.Fatal(query, got, err)
		}
	}
	var cipher, nonce []byte
	if err := db.QueryRowContext(ctx, `SELECT ciphertext,nonce FROM secrets WHERE bot_id=? AND name='KEY'`, id).Scan(&cipher, &nonce); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.ParseMasterKey(keyText)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Open(id, "KEY", cipher, nonce)
	if err != nil || string(plain) != secret {
		t.Fatal("secret was not restored correctly", err)
	}
	clear(plain)
	botDir := filepath.Join(dir, "bots", fmt.Sprint(id))
	if data, err := os.ReadFile(filepath.Join(botDir, "files", "data.txt")); err != nil || string(data) != "transferred data" {
		t.Fatal(string(data), err)
	}
	botDB, err := dbgate.OpenReadOnly(filepath.Join(botDir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer botDB.Close()
	var order int
	if err := botDB.QueryRow(`SELECT id FROM orders`).Scan(&order); err != nil || order != 42 {
		t.Fatal(order, err)
	}
}
