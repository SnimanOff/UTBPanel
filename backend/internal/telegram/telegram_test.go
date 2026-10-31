package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const token = "123456:AAE-secret_Token"

// fake is a minimal Bot API: it records calls and answers from a table.
type fake struct {
	mu      sync.Mutex
	calls   []call
	answers map[string]string
}

type call struct {
	method string
	ctype  string
	body   string
	fields map[string]string
	files  map[string]string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	prefix := "/bot" + token + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		w.WriteHeader(401)
		io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	c := call{method: strings.TrimPrefix(r.URL.Path, prefix), ctype: r.Header.Get("Content-Type"), fields: map[string]string{}, files: map[string]string{}}
	if strings.HasPrefix(c.ctype, "multipart/") {
		r.ParseMultipartForm(1 << 20)
		for k, v := range r.MultipartForm.Value {
			c.fields[k] = v[0]
		}
		for k, v := range r.MultipartForm.File {
			fh, _ := v[0].Open()
			b, _ := io.ReadAll(fh)
			c.files[k] = string(b)
		}
	} else {
		b, _ := io.ReadAll(r.Body)
		c.body = string(b)
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	ans, ok := f.answers[c.method]
	f.mu.Unlock()
	if !ok {
		ans = `{"ok":true,"result":true}`
	}
	io.WriteString(w, ans)
}

func newFake(t *testing.T, answers map[string]string) (*fake, *Client) {
	f := &fake{answers: answers}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &Client{Base: srv.URL, HTTP: srv.Client()}
}

func TestGetMe(t *testing.T) {
	_, c := newFake(t, map[string]string{"getMe": `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Echo","username":"echo_bot"}}`})
	u, err := c.GetMe(context.Background(), token)
	if err != nil || u.ID != 42 || u.Username != "echo_bot" {
		t.Fatalf("%+v %v", u, err)
	}
	_, err = c.GetMe(context.Background(), "1:wrong")
	te, ok := AsError(err)
	if !ok || te.Code != 401 || te.Description != "Unauthorized" || len(te.Raw) == 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorsWithParameters(t *testing.T) {
	_, c := newFake(t, map[string]string{
		"sendMessage": `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`,
		"sendPhoto":   `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded","parameters":{"migrate_to_chat_id":-100123}}`,
	})
	_, err := c.SendMessage(context.Background(), token, SendMessage{ChatID: 1, Text: "hi"})
	if te, ok := AsError(err); !ok || te.RetryAfter != 7 || te.Code != 429 {
		t.Fatalf("429: %v", err)
	}
	_, err = c.SendPhoto(context.Background(), token, SendPhoto{ChatID: 1, Photo: "file-id"})
	if te, ok := AsError(err); !ok || te.MigrateTo != -100123 {
		t.Fatalf("migrate: %v", err)
	}
}

func TestSetWebhookMultipart(t *testing.T) {
	f, c := newFake(t, nil)
	err := c.SetWebhook(context.Background(), token, SetWebhook{
		URL: "https://203.0.113.10:8443/hook/1", SecretToken: "s3cret",
		Certificate: InputFile{Name: "cert.pem", Data: []byte("PEM")}, MaxConnections: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := f.calls[0]
	if !strings.HasPrefix(got.ctype, "multipart/form-data") || got.files["certificate"] != "PEM" ||
		got.fields["url"] != "https://203.0.113.10:8443/hook/1" || got.fields["max_connections"] != "40" || got.fields["secret_token"] != "s3cret" {
		t.Fatalf("call = %+v", got)
	}
	// Without a certificate it is plain JSON.
	c.SetWebhook(context.Background(), token, SetWebhook{URL: "https://x/hook/1", DropPendingUpdates: true})
	var body map[string]any
	json.Unmarshal([]byte(f.calls[1].body), &body)
	if f.calls[1].ctype != "application/json" || body["drop_pending_updates"] != true || body["certificate"] != nil {
		t.Fatalf("json call = %+v", f.calls[1])
	}
}

func TestProfile(t *testing.T) {
	f, c := newFake(t, map[string]string{
		"getMyName":             `{"ok":true,"result":{"name":"Shop"}}`,
		"getMyDescription":      `{"ok":true,"result":{"description":"Long"}}`,
		"getMyShortDescription": `{"ok":true,"result":{"short_description":"Short"}}`,
		"getMyCommands":         `{"ok":true,"result":[{"command":"start","description":"Начать"}]}`,
	})
	p, err := c.GetProfile(context.Background(), token, "ru")
	if err != nil || p.Name != "Shop" || p.ShortDescription != "Short" || len(p.Commands) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	if !strings.Contains(f.calls[0].body, `"language_code":"ru"`) {
		t.Fatalf("language not sent: %s", f.calls[0].body)
	}
	c.SetMyCommands(context.Background(), token, "", nil)
	if last := f.calls[len(f.calls)-1]; last.body != `{"commands":[]}` {
		t.Fatalf("set commands body = %s", last.body)
	}
}

func TestTokenNeverInErrors(t *testing.T) {
	c := &Client{Base: "http://127.0.0.1:1"} // nothing listens there
	_, err := c.GetMe(context.Background(), token)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "secret_Token") || !strings.Contains(err.Error(), "/bot***") {
		t.Fatalf("token leaked: %v", err)
	}
	if MaskToken("https://api.telegram.org/bot"+token+"/getMe") != "https://api.telegram.org/bot***/getMe" {
		t.Fatal("MaskToken")
	}
}
