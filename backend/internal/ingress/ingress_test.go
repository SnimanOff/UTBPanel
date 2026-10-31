package ingress

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/updrules"
)

const msg = `{"update_id":1,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5,"username":"ann","language_code":"ru","is_premium":true},"text":"/start ads_vk"}}`

// echoBot answers with sendMessage in the body after checking the secret.
func echoBot(t *testing.T) (port int, calls *int) {
	n := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != "hook" {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		n++
		mu.Unlock()
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"method":"sendMessage","chat_id":5,"text":"echo"}`)
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port, &n
}

func TestParse(t *testing.T) {
	u := Parse(1, []byte(msg), time.Now())
	if u.Type != "message" || u.ChatType != "private" || u.UserID != 5 || u.Username != "ann" || u.Language != "ru" || !u.Premium || u.StartParam != "ads_vk" {
		t.Fatalf("%+v", u)
	}
	pay := Parse(1, []byte(`{"update_id":2,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"successful_payment":{"currency":"XTR","total_amount":50,"invoice_payload":"p","telegram_payment_charge_id":"c"}}}`), time.Now())
	if pay.Payment == nil || pay.Payment.Amount != 50 || pay.Payment.Currency != "XTR" {
		t.Fatalf("payment %+v", pay.Payment)
	}
	m := Parse(1, []byte(`{"update_id":3,"my_chat_member":{"chat":{"id":5,"type":"private"},"from":{"id":5},"new_chat_member":{"status":"kicked"}}}`), time.Now())
	if m.MemberStatus != "kicked" || m.UserID != 5 {
		t.Fatalf("member %+v", m)
	}
}

func TestForward(t *testing.T) {
	port, calls := echoBot(t)
	p := New(nil)
	p.Target = func(context.Context, int64) (Target, error) {
		return Target{Port: port, Secret: "hook", Running: true}, nil
	}
	var outs []Outcome
	p.Observe = append(p.Observe, func(o Outcome) { outs = append(outs, o) })
	accounted := false
	p.Account = append(p.Account, func(context.Context, *Update) { accounted = true }, func(context.Context, *Update) { panic("broken accounting") })
	rep := p.Handle(context.Background(), 1, []byte(msg), time.Now())
	if rep.Status != 200 || !strings.Contains(string(rep.Body), "echo") || *calls != 1 || !accounted {
		t.Fatalf("%+v calls=%d", rep, *calls)
	}
	if len(outs) != 1 || outs[0].Result != "delivered" || outs[0].Latency <= 0 {
		t.Fatalf("outcome %+v", outs)
	}
}

func TestChecksStop(t *testing.T) {
	port, calls := echoBot(t)
	p := New(nil)
	p.Target = func(context.Context, int64) (Target, error) {
		return Target{Port: port, Secret: "hook", Running: true}, nil
	}
	deferred := 0
	p.Defer = func(context.Context, *Update) error { deferred++; return nil }
	p.Checks = []Step{func(context.Context, *Update) Verdict {
		return Verdict{Stop: true, Reason: updrules.Maintenance, Text: "Техработы"}
	}}
	rep := p.Handle(context.Background(), 1, []byte(msg), time.Now())
	if !strings.Contains(string(rep.Body), "Техработы") || *calls != 0 {
		t.Fatalf("%s calls=%d", rep.Body, *calls)
	}
	pay := `{"update_id":2,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"successful_payment":{"total_amount":1}}}`
	p.Handle(context.Background(), 1, []byte(pay), time.Now())
	if deferred != 1 || *calls != 0 {
		t.Fatal("payment during maintenance must be deferred")
	}
	// Closed mode: the bot works, payments pass.
	p.Checks = []Step{func(context.Context, *Update) Verdict {
		return Verdict{Stop: true, Reason: updrules.Closed, Text: "закрыто"}
	}}
	p.Handle(context.Background(), 1, []byte(pay), time.Now())
	if *calls != 1 {
		t.Fatal("payment in closed mode must reach the bot")
	}
	// Ban: silent.
	p.Checks = []Step{func(context.Context, *Update) Verdict { return Verdict{Stop: true, Silent: true} }}
	if rep := p.Handle(context.Background(), 1, []byte(msg), time.Now()); len(rep.Body) != 0 {
		t.Fatal("ban must be silent")
	}
}

func TestTimeoutAndPanic(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) }))
	defer slow.Close()
	p := New(nil)
	p.BotTimeout = 100 * time.Millisecond
	p.Target = func(context.Context, int64) (Target, error) {
		return Target{Port: slow.Listener.Addr().(*net.TCPAddr).Port, Running: true}, nil
	}
	var res string
	p.Observe = []func(Outcome){func(o Outcome) { res = o.Result }}
	if rep := p.Handle(context.Background(), 1, []byte(msg), time.Now()); rep.Status != 200 || res != "timeout" {
		t.Fatalf("%+v %s", rep, res)
	}
	p.Target = func(context.Context, int64) (Target, error) { panic("boom") }
	if rep := p.Handle(context.Background(), 1, []byte(msg), time.Now()); rep.Status != 200 || res != "error" {
		t.Fatalf("panic: %+v %s", rep, res)
	}
}
