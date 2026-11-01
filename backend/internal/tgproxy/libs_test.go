//go:build libs

package tgproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Popular bot libraries: does their default client honour HTTPS_PROXY and
// SSL_CERT_FILE? A call that went through the proxy reaches the fake
// Telegram and gets the username "fakebot"; one that bypassed it does not.
//
//	go test -tags libs ./internal/tgproxy  (Linux, network for modules)
func TestLibrariesUseProxy(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Fake","username":"fakebot","can_join_groups":true}}`)
	}))
	defer up.Close()
	dir := t.TempDir()
	ca, err := LoadCA(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "bundle.pem")
	if err := ca.WriteBundle(bundle); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	p := &Proxy{CA: ca, Upstream: up.URL, UpstreamTLS: up.Client().Transport.(*http.Transport).TLSClientConfig}
	if err := p.Start(1, "main", port); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(1, "main")
	for _, lib := range []string{"gotgbot", "telebot", "tgbotapi"} {
		t.Run(lib, func(t *testing.T) {
			cmd := exec.Command("go", "run", "./"+lib)
			cmd.Dir = "libtest"
			cmd.Env = append(os.Environ(), "BOT_TOKEN=42:fake-token")
			for k, v := range Env(port, bundle) {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			out, err := cmd.CombinedOutput()
			got := strings.TrimSpace(string(out))
			if err != nil || !strings.Contains(got, "username: fakebot") {
				t.Errorf("%s does not go through the proxy: %v\n%s", lib, err, got)
			}
			t.Log(lib + ": " + got + " (port " + strconv.Itoa(port) + ")")
		})
	}
}
