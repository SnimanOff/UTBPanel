// Package updrules decides what happens to an update that does not reach
// the bot: maintenance, pause, closed mode, antiflood, bot unavailable,
// core down. The answer goes in the webhook response body, without a
// separate request to Telegram. Shared by the edge and the core.
package updrules

import (
	"encoding/json"
	"sync"
	"time"
)

// Kind of an update for these rules.
type Kind int

const (
	KindOther          Kind = iota // groups, channels, inline, edits…
	KindPrivateMessage             // message in a private chat
	KindCallback                   // button press
	KindPreCheckout                // pre_checkout_query
	KindPayment                    // successful_payment / refunded_payment
	KindMembership                 // my_chat_member, chat_join_request
)

// Reason why the update does not reach the bot.
type Reason int

const (
	Maintenance Reason = iota // maintenance or pause (also: core down)
	Closed                    // closed mode
	Antiflood
	Unavailable // the bot is not running
)

// Action decided for an update.
type Action int

const (
	Drop  Action = iota // 200 without a reply
	Reply               // 200 with a method in the body
	Defer               // keep in the queue, deliver to the bot later
	Pass                // deliver to the bot now (payments during ban etc.)
)

// Info is what the rules need from an update.
type Info struct {
	Kind       Kind
	UpdateID   int64
	ChatID     int64
	UserID     int64
	CallbackID string
	CheckoutID string
}

// Parse reads the fields of a raw update.
func Parse(body []byte) Info {
	var u struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			From *struct {
				ID int64 `json:"id"`
			} `json:"from"`
			SuccessfulPayment json.RawMessage `json:"successful_payment"`
			RefundedPayment   json.RawMessage `json:"refunded_payment"`
		} `json:"message"`
		CallbackQuery *struct {
			ID   string `json:"id"`
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
		} `json:"callback_query"`
		PreCheckoutQuery *struct {
			ID   string `json:"id"`
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
		} `json:"pre_checkout_query"`
		MyChatMember    json.RawMessage `json:"my_chat_member"`
		ChatJoinRequest json.RawMessage `json:"chat_join_request"`
	}
	json.Unmarshal(body, &u)
	i := Info{UpdateID: u.UpdateID}
	switch {
	case u.Message != nil:
		i.ChatID = u.Message.Chat.ID
		if u.Message.From != nil {
			i.UserID = u.Message.From.ID
		}
		switch {
		case len(u.Message.SuccessfulPayment) > 0 || len(u.Message.RefundedPayment) > 0:
			i.Kind = KindPayment
		case u.Message.Chat.Type == "private":
			i.Kind = KindPrivateMessage
		}
	case u.CallbackQuery != nil:
		i.Kind, i.CallbackID, i.UserID = KindCallback, u.CallbackQuery.ID, u.CallbackQuery.From.ID
	case u.PreCheckoutQuery != nil:
		i.Kind, i.CheckoutID, i.UserID = KindPreCheckout, u.PreCheckoutQuery.ID, u.PreCheckoutQuery.From.ID
	case len(u.MyChatMember) > 0 || len(u.ChatJoinRequest) > 0:
		i.Kind = KindMembership
	}
	return i
}

// Decision is the result: what to do and, for Reply, the response body.
type Decision struct {
	Action Action
	Body   []byte // JSON with "method"
}

// Limiter allows one message per chat per minute: under a flood the
// replies would hit Telegram's limits.
type Limiter struct {
	mu   sync.Mutex
	last map[int64]time.Time
	Now  func() time.Time
}

// NewLimiter returns an empty limiter.
func NewLimiter() *Limiter { return &Limiter{last: map[int64]time.Time{}, Now: time.Now} }

func (l *Limiter) allow(chat int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	if t, ok := l.last[chat]; ok && now.Sub(t) < time.Minute {
		return false
	}
	l.last[chat] = now
	if len(l.last) > 100_000 { // forget old chats
		for k, t := range l.last {
			if now.Sub(t) >= time.Minute {
				delete(l.last, k)
			}
		}
	}
	return true
}

func body(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Decide applies the rules. text is the text for users (maintenance, closed
// mode, antiflood warning or "bot unavailable"); empty text means no
// message. botWorks is true for closed mode and antiflood: the bot itself
// runs, so payments and service updates go to it at once.
func Decide(i Info, r Reason, text string, l *Limiter) Decision {
	botWorks := r == Closed || r == Antiflood
	switch i.Kind {
	case KindPayment, KindMembership:
		if botWorks {
			return Decision{Action: Pass}
		}
		return Decision{Action: Defer}
	case KindPrivateMessage:
		if text == "" || (l != nil && !l.allow(i.ChatID)) {
			return Decision{Action: Drop}
		}
		return Decision{Action: Reply, Body: body(map[string]any{"method": "sendMessage", "chat_id": i.ChatID, "text": text})}
	case KindCallback:
		m := map[string]any{"method": "answerCallbackQuery", "callback_query_id": i.CallbackID}
		if text != "" {
			m["text"] = text
			// Maintenance: a window with OK; closed mode and antiflood: a
			// short hint at the top.
			m["show_alert"] = r == Maintenance || r == Unavailable
		}
		return Decision{Action: Reply, Body: body(m)}
	case KindPreCheckout:
		// Telegram waits 10 s anyway; this way the user sees why.
		msg := text
		if msg == "" {
			msg = "Оплата сейчас недоступна"
		}
		return Decision{Action: Reply, Body: body(map[string]any{"method": "answerPreCheckoutQuery", "pre_checkout_query_id": i.CheckoutID, "ok": false, "error_message": msg})}
	}
	return Decision{Action: Drop}
}
