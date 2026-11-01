package github

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func tarGz(t *testing.T, files map[string][]byte, modes map[string]int64) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		mode := modes[name]
		if mode == 0 {
			mode = 0o644
		}
		tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg})
		tw.Write(data)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtract(t *testing.T) {
	data := tarGz(t, map[string][]byte{
		"bot_1.0/README.md":    []byte("readme"),
		"bot_1.0/tgpanel.yaml": []byte("name: echo\n"),
		"bot_1.0/bot":          []byte("ELF..."),
	}, map[string]int64{"bot_1.0/bot": 0o755})
	var got string
	cfg, err := Extract(bytes.NewReader(data), func(name string, r io.Reader) error {
		b, _ := io.ReadAll(r)
		got = name + ":" + string(b)
		return nil
	})
	if err != nil || got != "bot:ELF..." || string(cfg) != "name: echo\n" {
		t.Fatalf("%q %q %v", got, cfg, err)
	}
	_, err = Extract(bytes.NewReader(tarGz(t, map[string][]byte{"a.txt": []byte("x")}, nil)), func(string, io.Reader) error { return nil })
	if err != ErrNoBinary {
		t.Fatalf("no executable: %v", err)
	}
}

func TestPickAndChecksums(t *testing.T) {
	r := Release{Assets: []Asset{{Name: "bot_darwin_arm64.tar.gz"}, {Name: "bot_linux_amd64.tar.gz"}, {Name: "bot_linux_amd64.tar.gz.sig"}, {Name: "checksums.txt"}}}
	bin, sums := Pick(r, "*linux_amd64*")
	if bin == nil || bin.Name != "bot_linux_amd64.tar.gz" || sums == nil {
		t.Fatalf("%v %v", bin, sums)
	}
	m := Checksums(strings.NewReader(strings.Repeat("a", 64) + "  bot_linux_amd64.tar.gz\n" + strings.Repeat("b", 64) + " *other\nbad line\n"))
	if m["bot_linux_amd64.tar.gz"] != strings.Repeat("a", 64) || m["other"] == "" || len(m) != 2 {
		t.Fatalf("%v", m)
	}
	if Version("release/v1.2") != "release-v1.2" || !ValidRepo("me/bot.go") || ValidRepo("me/../x") {
		t.Fatal("version or repo")
	}
}

func TestSignature(t *testing.T) {
	body := []byte(`{"action":"published"}`)
	h := Sign([]byte("s3cret"), body)
	if !ValidSignature([]byte("s3cret"), body, h) || ValidSignature([]byte("other"), body, h) || ValidSignature([]byte("s3cret"), body, "sha1=00") {
		t.Fatal("signature check")
	}
}

func TestReleasesETagAndMeta(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/me/bot/releases":
			calls++
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(404)
				io.WriteString(w, `{"message":"Not Found"}`)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			io.WriteString(w, `[{"tag_name":"v1.0.0","assets":[{"name":"a","url":"x"}]}]`)
		case "/meta":
			io.WriteString(w, `{"hooks":["192.30.252.0/22","2a0a:a440::/29"]}`)
		}
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, HTTP: srv.Client()}
	ctx := context.Background()
	if _, _, _, err := c.Releases(ctx, "me/bot", "", ""); err == nil || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("private repo without token: %v", err)
	}
	rels, etag, same, err := c.Releases(ctx, "me/bot", "tok", "")
	if err != nil || same || etag != `"v1"` || rels[0].Tag != "v1.0.0" {
		t.Fatalf("%v %q %v %v", rels, etag, same, err)
	}
	if _, _, same, err := c.Releases(ctx, "me/bot", "tok", etag); !same || err != nil {
		t.Fatal("304 must come back as not modified")
	}
	nets, err := c.HookNets(ctx)
	if err != nil || len(nets) != 2 || !nets[0].Contains(netip.MustParseAddr("192.30.252.10")) {
		t.Fatalf("%v %v", nets, err)
	}
}
