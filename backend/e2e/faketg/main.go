// Command faketg is a stand-in Bot API for end-to-end tests only. Tokens
// "<id>:ok…" are valid. It remembers webhooks set by the core and can send
// updates to them the way Telegram does (POST with the secret header),
// returning the webhook's answer.
//
//	POST /_send?token=…   body = update → {"status":…,"body":…}
//	GET  /_calls          methods called so far
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

type hook struct {
	URL    string
	Secret string
}

var (
	mu    sync.Mutex
	hooks = map[string]hook{}
	calls []string
)

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// params reads JSON or multipart (setWebhook with a certificate).
func params(r *http.Request) map[string]string {
	out := map[string]string{}
	mt, p, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "multipart/form-data" {
		mr := multipart.NewReader(r.Body, p["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(part)
			out[part.FormName()] = string(b)
		}
		return out
	}
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		} else {
			b, _ := json.Marshal(v)
			out[k] = string(b)
		}
	}
	return out
}

func api(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/bot"), "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	token, method := parts[0], parts[1]
	p := params(r)
	mu.Lock()
	calls = append(calls, method)
	mu.Unlock()
	if !strings.Contains(token, ":ok") {
		w.WriteHeader(401)
		reply(w, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	id := strings.SplitN(token, ":", 2)[0]
	switch method {
	case "getMe":
		reply(w, map[string]any{"ok": true, "result": map[string]any{"id": json.Number(id), "is_bot": true, "first_name": "Bot", "username": "bot" + id}})
	case "setWebhook":
		mu.Lock()
		hooks[token] = hook{URL: p["url"], Secret: p["secret_token"]}
		mu.Unlock()
		reply(w, map[string]any{"ok": true, "result": true})
	case "deleteWebhook":
		mu.Lock()
		delete(hooks, token)
		mu.Unlock()
		reply(w, map[string]any{"ok": true, "result": true})
	case "getWebhookInfo":
		mu.Lock()
		h := hooks[token]
		mu.Unlock()
		reply(w, map[string]any{"ok": true, "result": map[string]any{"url": h.URL, "pending_update_count": 0}})
	default:
		reply(w, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	}
}

var client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
	// The edge uses a self-signed certificate; Telegram gets it with
	// setWebhook, the fake simply trusts it.
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
}}

func send(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	mu.Lock()
	h, ok := hooks[token]
	mu.Unlock()
	if !ok {
		http.Error(w, "no webhook for this token", 404)
		return
	}
	body, _ := io.ReadAll(r.Body)
	req, _ := http.NewRequest("POST", h.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", h.Secret)
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	reply(w, map[string]any{"status": resp.StatusCode, "body": string(b)})
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/bot") {
			api(w, r)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("POST /_send", send)
	mux.HandleFunc("GET /_calls", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reply(w, calls)
	})
	log.Println("fake telegram on :8081")
	log.Fatal(http.ListenAndServe(":8081", mux))
}
