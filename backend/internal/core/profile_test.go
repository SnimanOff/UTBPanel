package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
)

func TestProfile(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)
	b, err := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		body, _ := io.ReadAll(r.Body)
		switch method {
		case "getMyName":
			io.WriteString(w, `{"ok":true,"result":{"name":"Эхо"}}`)
		case "getMyDescription":
			io.WriteString(w, `{"ok":true,"result":{"description":"d"}}`)
		case "getMyShortDescription":
			io.WriteString(w, `{"ok":true,"result":{"short_description":"s"}}`)
		case "getMyCommands":
			io.WriteString(w, `{"ok":true,"result":[{"command":"start","description":"Начать"}]}`)
		case "getChatMenuButton":
			io.WriteString(w, `{"ok":true,"result":{"type":"commands"}}`)
		case "setMyName":
			w.WriteHeader(429)
			io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 3600","parameters":{"retry_after":3600}}`)
		default:
			sent = append(sent, method+" "+string(body))
			io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	defer srv.Close()
	c.TG.Base = srv.URL

	v, err := c.Profile(ctx, b.ID, "en")
	if err != nil || v.Profile.Name != "Эхо" || len(v.Profile.Commands) != 1 || v.Menu.Type != "commands" {
		t.Fatalf("%+v %v", v, err)
	}
	name, desc := "New", "about"
	cmds := []telegram.BotCommand{{Command: "help", Description: "Помощь"}}
	res, err := c.SaveProfile(ctx, ProfileReq{BotID: b.ID, Lang: "en", Name: &name, Description: &desc, Commands: &cmds})
	if err != nil {
		t.Fatal(err)
	}
	if fe, ok := res.Errors["name"]; !ok || fe.RetryAt.IsZero() {
		t.Fatalf("name must fail with a retry time: %+v", res)
	}
	if len(sent) != 2 || !strings.Contains(sent[0], `"language_code":"en"`) {
		t.Fatalf("sent %v", sent)
	}
	bad := []telegram.BotCommand{{Command: "Bad Cmd"}}
	res, _ = c.SaveProfile(ctx, ProfileReq{BotID: b.ID, Commands: &bad})
	if _, ok := res.Errors["commands"]; !ok || len(sent) != 2 {
		t.Fatal("invalid command must be refused before Telegram")
	}
}
