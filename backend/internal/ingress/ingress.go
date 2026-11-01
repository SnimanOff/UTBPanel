// Package ingress is the update pipeline of the core: every webhook the
// edge accepted goes through parsing, accounting, the checks (ban, closed
// mode, maintenance, antiflood) and is forwarded to the bot. Accounting and
// metrics can never stop delivery: if they fail, the update still goes to
// the bot.
package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/updrules"
)

// Update is one parsed update.
type Update struct {
	BotID    int64
	Body     []byte
	Received time.Time
	updrules.Info
	// Fields for accounting.
	Type       string // message, callback_query, …
	ChatType   string // private, group, supergroup, channel
	Username   string
	Language   string
	Premium    bool
	StartParam string // payload of /start <param>
	Start      bool   // the message is /start
	Payment    *Payment
	// MyChatMember status of the bot in a private chat: kicked / member.
	MemberStatus string
}

// Payment is a successful or refunded payment.
type Payment struct {
	Currency string `json:"currency"`
	Amount   int64  `json:"total_amount"`
	Payload  string `json:"invoice_payload"`
	ChargeID string `json:"telegram_payment_charge_id"`
	Refund   bool   `json:"-"`
}

// Verdict of a check step: Stop keeps the update from the bot.
type Verdict struct {
	Stop   bool
	Reason updrules.Reason
	Text   string
	// Silent: 200 without any reply and without the rules (bans).
	Silent bool
}

// Step is a check on the way to the bot (ban, closed mode, maintenance,
// antiflood). The first Stop wins.
type Step func(ctx context.Context, u *Update) Verdict

// Target is where the bot listens.
type Target struct {
	Port    int
	Secret  string
	Running bool
}

// Outcome is reported to metrics for every update.
type Outcome struct {
	Update  *Update
	Result  string // delivered | dropped_<reason> | deferred | replied | error | timeout | unavailable
	Latency time.Duration
	Status  int
	// ReplyMethod: the bot answered with a method in the webhook body.
	ReplyMethod bool
}

// Pipeline processes updates.
type Pipeline struct {
	Log *slog.Logger
	// Target returns the bot's port and webhook secret.
	Target func(ctx context.Context, botID int64) (Target, error)
	// Account records users, sources, blocks and payments (steps 5–6).
	Account []func(ctx context.Context, u *Update)
	// Checks are steps 7–10 in order.
	Checks []Step
	// Defer keeps a payment or service update for later delivery.
	Defer func(ctx context.Context, u *Update) error
	// Unavailable handles a bot that is not running (wait, then the rules).
	Unavailable func(ctx context.Context, u *Update) (edge.Reply, bool)
	// Observe receives the outcome (metrics, step 12).
	Observe []func(Outcome)
	// BotTimeout bounds the bot's answer.
	BotTimeout time.Duration
	// Gate holds new updates while deferred ones are being delivered; it
	// returns the release function.
	Gate func(botID int64) func()

	Replies  *updrules.Limiter
	client   http.Client
	inflight sync.Map // botID → *atomic.Int64
}

// New returns a pipeline with default settings.
func New(log *slog.Logger) *Pipeline {
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{
		Log:        log,
		BotTimeout: 30 * time.Second,
		Replies:    updrules.NewLimiter(),
		client: http.Client{Transport: &http.Transport{
			Proxy:               nil, // straight to 127.0.0.1
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
			DialContext:         (&net.Dialer{Timeout: time.Second}).DialContext,
		}},
	}
}

// InFlight returns how many updates the bot is processing right now.
func (p *Pipeline) InFlight(botID int64) int64 {
	if v, ok := p.inflight.Load(botID); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

func (p *Pipeline) counter(botID int64) *atomic.Int64 {
	v, _ := p.inflight.LoadOrStore(botID, new(atomic.Int64))
	return v.(*atomic.Int64)
}

// Parse fills the update fields (step 4).
func Parse(botID int64, body []byte, received time.Time) *Update {
	u := &Update{BotID: botID, Body: body, Received: received, Info: updrules.Parse(body)}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return u
	}
	for k := range raw {
		if k != "update_id" {
			u.Type = k
			break
		}
	}
	type user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Language string `json:"language_code"`
		Premium  bool   `json:"is_premium"`
	}
	var obj struct {
		From *user `json:"from"`
		Chat *struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		Text              string          `json:"text"`
		SuccessfulPayment *Payment        `json:"successful_payment"`
		RefundedPayment   *Payment        `json:"refunded_payment"`
		NewChatMember     json.RawMessage `json:"new_chat_member"`
		Message           *struct {
			Chat struct {
				Type string `json:"type"`
			} `json:"chat"`
		} `json:"message"`
	}
	json.Unmarshal(raw[u.Type], &obj)
	if obj.From != nil {
		u.UserID, u.Username, u.Language, u.Premium = obj.From.ID, obj.From.Username, obj.From.Language, obj.From.Premium
	}
	if obj.Chat != nil {
		u.ChatType = obj.Chat.Type
		if u.ChatID == 0 {
			u.ChatID = obj.Chat.ID
		}
	} else if obj.Message != nil {
		u.ChatType = obj.Message.Chat.Type
	} else if obj.From != nil {
		u.ChatType = "private"
	}
	if u.Type == "message" && u.ChatType == "private" {
		u.StartParam, u.Start = startParam(obj.Text)
	}
	if obj.SuccessfulPayment != nil {
		u.Payment = obj.SuccessfulPayment
	} else if obj.RefundedPayment != nil {
		u.Payment = obj.RefundedPayment
		u.Payment.Refund = true
	}
	if u.Type == "my_chat_member" && u.ChatType == "private" {
		var m struct {
			Status string `json:"status"`
		}
		json.Unmarshal(obj.NewChatMember, &m)
		u.MemberStatus = m.Status
	}
	return u
}

// startParam returns the parameter of "/start <param>"; ok is true for any
// /start.
func startParam(text string) (string, bool) {
	if text != "/start" && !(len(text) > 7 && text[:7] == "/start ") {
		return "", false
	}
	if len(text) > 7 {
		return text[7:], true
	}
	return "", true
}

// Handle runs the pipeline for one update. A panic loses only this update.
func (p *Pipeline) Handle(ctx context.Context, botID int64, body []byte, received time.Time) (rep edge.Reply) {
	u := Parse(botID, body, received)
	out := Outcome{Update: u}
	defer func() {
		if r := recover(); r != nil {
			p.Log.Error("panic in update pipeline", "bot", botID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			rep, out.Result = edge.Reply{Status: http.StatusOK}, "error"
		}
		out.Status = rep.Status
		for _, fn := range p.Observe {
			p.safe("observe", func() { fn(out) })
		}
	}()

	for _, fn := range p.Account {
		p.safe("account", func() { fn(ctx, u) })
	}

	for _, check := range p.Checks {
		var v Verdict
		p.safe("check", func() { v = check(ctx, u) })
		if !v.Stop {
			continue
		}
		out.Result = "dropped_" + reasonName(v.Reason)
		if v.Silent {
			return edge.Reply{Status: http.StatusOK}
		}
		d := updrules.Decide(u.Info, v.Reason, v.Text, p.Replies)
		switch d.Action {
		case updrules.Reply:
			return edge.Reply{Status: http.StatusOK, ContentType: "application/json", Body: d.Body}
		case updrules.Defer:
			if p.Defer != nil {
				if err := p.Defer(ctx, u); err != nil {
					p.Log.Error("deferring update failed", "bot", botID, "err", err)
					return edge.Reply{Status: http.StatusInternalServerError}
				}
			}
			out.Result = "deferred"
			return edge.Reply{Status: http.StatusOK}
		case updrules.Pass:
			out.Result = ""
		default:
			return edge.Reply{Status: http.StatusOK}
		}
		if out.Result == "" {
			break // payments and service updates go on to the bot
		}
	}

	if p.Gate != nil {
		release := p.Gate(botID)
		defer release()
	}
	rep, res, lat := p.forward(ctx, u)
	if res == "unavailable" && p.Unavailable != nil {
		if r, ok := p.Unavailable(ctx, u); ok {
			out.Result = "unavailable"
			return r
		}
		// The bot came back: try once more.
		rep, res, lat = p.forward(ctx, u)
	}
	out.Result, out.Latency = res, lat
	out.ReplyMethod = bytes.Contains(rep.Body, []byte(`"method"`))
	return rep
}

func reasonName(r updrules.Reason) string {
	switch r {
	case updrules.Maintenance:
		return "maintenance"
	case updrules.Closed:
		return "closed"
	case updrules.Antiflood:
		return "antiflood"
	case updrules.Unavailable:
		return "unavailable"
	}
	return "ban"
}

func (p *Pipeline) safe(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			p.Log.Error("panic in pipeline step", "step", what, "panic", fmt.Sprint(r))
		}
	}()
	fn()
}

// Deliver forwards a stored update straight to the bot, without the checks.
func (p *Pipeline) Deliver(ctx context.Context, botID int64, body []byte) edge.Reply {
	u := Parse(botID, body, time.Now())
	rep, res, _ := p.forward(ctx, u)
	if res == "unavailable" || res == "timeout" || res == "error" {
		rep.Status = http.StatusServiceUnavailable
	}
	return rep
}

// ErrNotRunning is returned by Target for a bot without a process.
var ErrNotRunning = errors.New("ingress: bot is not running")

// forward sends the update to the bot (step 11). The bot's answer,
// including a method in the body, goes to Telegram as is.
func (p *Pipeline) forward(ctx context.Context, u *Update) (edge.Reply, string, time.Duration) {
	t, err := p.Target(ctx, u.BotID)
	if err != nil || !t.Running {
		return edge.Reply{Status: http.StatusOK}, "unavailable", 0
	}
	ctx, cancel := context.WithTimeout(ctx, p.BotTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(t.Port)+"/", bytes.NewReader(u.Body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", t.Secret)
	c := p.counter(u.BotID)
	c.Add(1)
	start := time.Now()
	resp, err := p.client.Do(req)
	lat := time.Since(start)
	c.Add(-1)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// Telegram would retry a failed webhook; the bot is slow, not
			// gone, so answer 200 and count a timeout.
			return edge.Reply{Status: http.StatusOK}, "timeout", lat
		}
		var oe *net.OpError
		if errors.As(err, &oe) && oe.Op == "dial" {
			return edge.Reply{Status: http.StatusOK}, "unavailable", lat
		}
		return edge.Reply{Status: http.StatusBadGateway}, "error", lat
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	res := "delivered"
	if resp.StatusCode >= 500 {
		res = "error"
	}
	return edge.Reply{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body}, res, lat
}
