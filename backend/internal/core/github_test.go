package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/github"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
)

func linuxBot(t *testing.T) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "bot")
	cmd := exec.Command("go", "build", "-o", out, "../runner/testdata/testbot")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	data, _ := os.ReadFile(out)
	return data
}

func TestGitHubRelease(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})

	bin := linuxBot(t)
	var arch bytes.Buffer
	gz := gzip.NewWriter(&arch)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "bot", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
	tw.Write(bin)
	cfg := []byte("name: echo\n")
	tw.WriteHeader(&tar.Header{Name: "tgpanel.yaml", Mode: 0o644, Size: int64(len(cfg)), Typeflag: tar.TypeReg})
	tw.Write(cfg)
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(arch.Bytes())
	sums := hex.EncodeToString(sum[:]) + "  bot_linux_amd64.tar.gz\n"
	badSums := false

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp_x" {
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		switch r.URL.Path {
		case "/repos/me/echo/releases":
			fmt.Fprintf(w, `[{"tag_name":"v2.0.0-rc1","prerelease":true,"assets":[]},
				{"tag_name":"v1.0.0","assets":[{"name":"bot_linux_amd64.tar.gz","url":"%[1]s/a/1"},{"name":"checksums.txt","url":"%[1]s/a/2"}]}]`, srv.URL)
		case "/a/1":
			w.Write(arch.Bytes())
		case "/a/2":
			if badSums {
				io.WriteString(w, strings.Repeat("0", 64)+"  bot_linux_amd64.tar.gz\n")
				return
			}
			io.WriteString(w, sums)
		}
	}))
	defer srv.Close()
	c.GH = &github.Client{Base: srv.URL, HTTP: srv.Client()}

	tok := "ghp_x"
	saved, err := c.SetGitHub(ctx, GitHubSet{BotID: b.ID, Repo: "https://github.com/me/echo", Token: &tok})
	if err != nil || saved.HookSecret == "" || saved.Source.Repo != "me/echo" || !saved.Source.HasToken {
		t.Fatalf("%+v %v", saved, err)
	}
	// The panel's secrets never reach the bot or its list.
	if all, _ := c.Sec.DecryptAll(ctx, b.ID); len(all) != 2 { // BOT_TOKEN, WEBHOOK_SECRET
		t.Fatalf("bot gets %d secrets", len(all))
	}
	if again, _ := c.SetGitHub(ctx, GitHubSet{BotID: b.ID, Repo: "me/echo"}); again.HookSecret != "" || !again.Source.HasToken {
		t.Fatal("the webhook secret is shown only once; nil token keeps it")
	}

	badSums = true
	if err := c.CheckGitHub(ctx, b.ID); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("wrong checksum must refuse: %v", err)
	}
	badSums = false
	c.DB.W.Exec(`UPDATE github_sources SET etag = ''`)
	if err := c.CheckGitHub(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if !c.Releases.Exists(b.ID, "v1.0.0") {
		t.Fatal("release not imported; the prerelease must be skipped")
	}
	if y, _ := os.ReadFile(filepath.Join(c.Releases.Dir(b.ID), "v1.0.0", "tgpanel.yaml")); string(y) != string(cfg) {
		t.Fatalf("tgpanel.yaml = %q", y)
	}
	g, _ := c.GitHub(ctx, b.ID)
	if g.LastTag != "v1.0.0" || g.LastError != "" || !strings.HasSuffix(g.HookURL, fmt.Sprintf("/gh/%d", b.ID)) {
		t.Fatalf("%+v", g)
	}

	// Webhooks: wrong signature, ping, release.
	secret, _ := c.Sec.Decrypt(ctx, b.ID, secrets.GitHubHookName)
	body := []byte(`{"action":"published"}`)
	if st, _ := c.onGitHubHook(ctx, edge.GitHubHook{BotID: b.ID, Event: "release", Signature: github.Sign([]byte("x"), body), Body: body}); st != 401 {
		t.Fatalf("bad signature: %d", st)
	}
	if st, _ := c.onGitHubHook(ctx, edge.GitHubHook{BotID: b.ID, Event: "ping", Signature: github.Sign(secret, body), Body: body}); st != 200 {
		t.Fatalf("ping: %d", st)
	}
	if st, _ := c.onGitHubHook(ctx, edge.GitHubHook{BotID: b.ID, Event: "release", Signature: github.Sign(secret, body), Body: body}); st != 202 {
		t.Fatalf("release: %d", st)
	}

	// An empty repository turns GitHub off and drops its secrets.
	c.SetGitHub(ctx, GitHubSet{BotID: b.ID})
	if g, _ := c.GitHub(ctx, b.ID); g != nil {
		t.Fatal("still configured")
	}
	if _, err := c.Sec.Decrypt(ctx, b.ID, secrets.GitHubHookName); err == nil {
		t.Fatal("webhook secret left")
	}
}
