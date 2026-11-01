//go:build e2e

// Package e2e runs against a live docker compose stack (see run.sh). Each
// phase is one test; run.sh stops and kills things between them.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/users"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

const token = "4242:ok-e2e"

var actor = journal.Actor{Source: journal.SourceCLI, Name: "e2e"}

func dial(t *testing.T) *wire.Conn {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		c, err := wire.Dial(ctx, wire.CoreSock(os.Getenv("TGPANEL_RUN_DIR")), nil, app.Hello(wire.ProtoCoreCLI, "e2e", 1, 1), nil)
		cancel()
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("core socket: %v", err)
		}
		time.Sleep(time.Second)
	}
}

func call(t *testing.T, c *wire.Conn, method string, in, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := c.Call(ctx, method, in, out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

// send makes the fake Telegram deliver an update and returns the webhook
// answer body.
func send(t *testing.T, update string) (int, string) {
	t.Helper()
	resp, err := http.Post(os.Getenv("FAKETG")+"/_send?token="+token, "application/json", strings.NewReader(update))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("send: %d %s", resp.StatusCode, b)
	}
	var r struct {
		Status int
		Body   string
	}
	json.Unmarshal(b, &r)
	return r.Status, r.Body
}

func message(user int, text string) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":1,"date":0,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"A","username":"user%d","language_code":"ru"},"text":%q}}`,
		time.Now().UnixNano()%1e9, user, user, user, text)
}

func payment(user int) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":2,"date":0,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"A"},"successful_payment":{"currency":"XTR","total_amount":5,"invoice_payload":"p","telegram_payment_charge_id":"c%d"}}}`,
		time.Now().UnixNano()%1e9, user, user, time.Now().UnixNano())
}

func botID(t *testing.T, c *wire.Conn) int64 {
	var bs []core.Bot
	call(t, c, "bots.list", struct{}{}, &bs)
	for _, b := range bs {
		if b.TgID == 4242 {
			return b.ID
		}
	}
	return 0
}

func eventually(t *testing.T, what string, timeout time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// TestDelivery: create the bot, upload the echo bot, start it; an update
// goes edge → core → bot and the answer comes back in the webhook response.
func TestDelivery(t *testing.T) {
	c := dial(t)
	defer c.Close()
	var b core.Bot
	call(t, c, "bots.create", core.CreateBot{Name: "echo", Token: token, Actor: actor}, &b)

	bin := "/tmp/echo-bot"
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = "../../examples/echo-bot"
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build echo bot: %v %s", err, out)
	}
	data, _ := os.ReadFile(bin)
	var id string
	call(t, c, "releases.begin", core.UploadBegin{BotID: b.ID, Version: "v1", Actor: actor}, &id)
	for off := 0; off < len(data); off += 256 << 10 {
		call(t, c, "releases.chunk", core.UploadChunk{ID: id, Data: data[off:min(off+256<<10, len(data))]}, nil)
	}
	var rel deploy.Release
	call(t, c, "releases.finish", core.UploadFinish{ID: id, BotID: b.ID, Actor: actor}, &rel)
	call(t, c, "releases.switch", core.SwitchVersion{BotID: b.ID, Version: "v1", Actor: actor}, nil)

	var body string
	eventually(t, "echo answer", 30*time.Second, func() bool {
		_, body = send(t, message(77, "hello"))
		return strings.Contains(body, "hello (#")
	})
	if !strings.Contains(body, `"method":"sendMessage"`) {
		t.Fatalf("answer = %s", body)
	}
	// Users are written every 10 s.
	eventually(t, "user accounted", 20*time.Second, func() bool {
		var res users.ListResult
		call(t, c, "users.list", users.ListQuery{BotID: b.ID}, &res)
		return res.Total == 1 && res.Users[0].ID == 77 && res.Users[0].Username == "user77"
	})
}

// TestCoreDown: the core is stopped; the edge answers by itself and queues
// the payment.
func TestCoreDown(t *testing.T) {
	_, body := send(t, message(77, "anyone?"))
	if !strings.Contains(body, "sendMessage") || !strings.Contains(body, "Бот") {
		t.Fatalf("edge answer = %s", body)
	}
	status, body := send(t, payment(77))
	if status != 200 || body != "" {
		t.Fatalf("payment: %d %s", status, body)
	}
}

// TestQueueHandedOver: after the core starts it takes the edge's queue and
// delivers it to the bot once the bot runs again.
func TestQueueHandedOver(t *testing.T) {
	c := dial(t)
	defer c.Close()
	id := botID(t, c)
	if id == 0 {
		t.Fatal("bot lost after core restart")
	}
	eventually(t, "bot back and queue delivered", 60*time.Second, func() bool {
		var n int
		call(t, c, "ingress.deferred_count", id, &n)
		var st runner.Status
		c.Call(context.Background(), "bots.status", id, &st)
		return n == 0 && st.Running
	})
	_, body := send(t, message(77, "back"))
	if !strings.Contains(body, "back (#") {
		t.Fatalf("after restart: %s", body)
	}
}

// TestBotCrash: the bot process was killed; the core restarts it within a
// second or two and the update waits for it, or the user gets "bot
// unavailable" — never silence.
func TestBotCrash(t *testing.T) {
	c := dial(t)
	defer c.Close()
	_, body := send(t, message(78, "after crash"))
	if !strings.Contains(body, "after crash (#") && !strings.Contains(body, "перезапускается") {
		t.Fatalf("answer after crash: %s", body)
	}
	id := botID(t, c)
	eventually(t, "restarted", 30*time.Second, func() bool {
		var st runner.Status
		c.Call(context.Background(), "bots.status", id, &st)
		return st.Running && st.Restarts >= 1
	})
	// The webhook was set again on start.
	resp, err := http.Get(os.Getenv("FAKETG") + "/_calls")
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	json.NewDecoder(resp.Body).Decode(&calls)
	resp.Body.Close()
	n := 0
	for _, m := range calls {
		if m == "setWebhook" {
			n++
		}
	}
	if n < 2 {
		t.Fatalf("setWebhook calls = %d", n)
	}
}
