package gate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

// Note is a panel notification for the tray.
type Note struct {
	ID       int64  `json:"id"`
	Text     string `json:"text"`
	Severity string `json:"severity"`
	Href     string `json:"href"`
}

// Watch follows the panel's notification stream until ctx ends: on gets
// every new notification, online reports whether the panel answers.
func (g *Gate) Watch(ctx context.Context, on func(Note), online func(bool)) {
	wait := 5 * time.Second
	last := true
	set := func(ok bool) {
		if ok != last && online != nil {
			online(ok)
		}
		last = ok
	}
	for ctx.Err() == nil {
		err := g.stream(ctx, on, func() { set(true); wait = 5 * time.Second })
		if ctx.Err() != nil {
			return
		}
		if err != errNotEnrolled {
			set(false)
			g.log.Warn("notification stream lost", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

var errNotEnrolled = fmt.Errorf("gate: not registered yet")

func (g *Gate) stream(ctx context.Context, on func(Note), connected func()) error {
	g.mu.Lock()
	tr, server := g.tr, g.server
	g.mu.Unlock()
	if tr == nil {
		return errNotEnrolled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+server+"/api/notifications/stream", nil)
	if err != nil {
		return err
	}
	// The panel accepts only requests that come through gate (local Host).
	req.Host = g.cfg.Listen
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gate: stream: %s", resp.Status)
	}
	connected()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && event == "notification":
			var n Note
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &n) == nil && on != nil {
				on(n)
			}
		case line == "":
			event = ""
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("gate: stream closed")
}

// OpenPage opens a gate page (/__gate/…) in the browser with a fresh login.
func (g *Gate) OpenPage(path string) {
	if g.cfg.OpenBrowser != nil {
		g.cfg.OpenBrowser(g.LoginURL() + "&next=" + path)
	}
}

// serverPage changes the panel address (the server moved).
func (g *Gate) serverPage(w http.ResponseWriter, r *http.Request) {
	msg := ""
	if r.Method == http.MethodPost {
		addr := strings.TrimSpace(r.FormValue("addr"))
		if err := g.SetServer(addr); err != nil {
			msg = `<p class="err">` + html.EscapeString(err.Error()) + `</p>`
		} else {
			msg = `<p>Сохранено. <a href="/">Открыть панель</a></p>`
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, serverPage, html.EscapeString(g.Server()), msg)
}

const serverPage = `<!doctype html><meta charset="utf-8"><title>TG Panel · адрес сервера</title>
<style>
body{background:#0c0a0f;color:#e9e4ee;font:16px "IBM Plex Sans",sans-serif;margin:0;padding:48px}
.box{border:1px solid #2b2236;background:#15111b;padding:32px;max-width:560px}
input{background:#0c0a0f;color:#e9e4ee;border:1px solid #2b2236;padding:8px;font:16px monospace;width:100%%;box-sizing:border-box}
button{margin-top:16px;background:#6b2fa3;color:#fff;border:0;padding:8px 16px}
.muted{color:#8b8195}.err{color:#c8102e}a{color:#8a45c8}
</style>
<div class="box">
<div class="muted">Адрес сервера панели (host:port). Ключ устройства и сертификат сервера не меняются.</div>
<form method="post"><input name="addr" value="%s"><button>Сохранить</button></form>
%s
</div>`
