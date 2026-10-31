package updrules

import (
	"encoding/json"
	"testing"
	"time"
)

var updates = map[Kind]string{
	KindPrivateMessage: `{"update_id":1,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"text":"hi"}}`,
	KindOther:          `{"update_id":2,"message":{"chat":{"id":-100,"type":"supergroup"},"from":{"id":5},"text":"hi"}}`,
	KindCallback:       `{"update_id":3,"callback_query":{"id":"cb1","from":{"id":5}}}`,
	KindPreCheckout:    `{"update_id":4,"pre_checkout_query":{"id":"pc1","from":{"id":5}}}`,
	KindPayment:        `{"update_id":5,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"successful_payment":{"total_amount":100}}}`,
	KindMembership:     `{"update_id":6,"my_chat_member":{"chat":{"id":5}}}`,
}

func TestParse(t *testing.T) {
	for k, u := range updates {
		if got := Parse([]byte(u)).Kind; got != k {
			t.Errorf("%s: kind %d, want %d", u, got, k)
		}
	}
	if i := Parse([]byte(updates[KindCallback])); i.CallbackID != "cb1" || i.UserID != 5 {
		t.Fatalf("%+v", i)
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		kind   Kind
		reason Reason
		want   Action
		method string
		alert  bool
	}{
		{KindPrivateMessage, Maintenance, Reply, "sendMessage", false},
		{KindPrivateMessage, Closed, Reply, "sendMessage", false},
		{KindPrivateMessage, Unavailable, Reply, "sendMessage", false},
		{KindCallback, Maintenance, Reply, "answerCallbackQuery", true},
		{KindCallback, Closed, Reply, "answerCallbackQuery", false},
		{KindCallback, Antiflood, Reply, "answerCallbackQuery", false},
		{KindPreCheckout, Maintenance, Reply, "answerPreCheckoutQuery", false},
		{KindPreCheckout, Closed, Reply, "answerPreCheckoutQuery", false},
		{KindPayment, Maintenance, Defer, "", false},
		{KindPayment, Unavailable, Defer, "", false},
		{KindPayment, Closed, Pass, "", false},
		{KindPayment, Antiflood, Pass, "", false},
		{KindMembership, Maintenance, Defer, "", false},
		{KindMembership, Antiflood, Pass, "", false},
		{KindOther, Maintenance, Drop, "", false},
		{KindOther, Closed, Drop, "", false},
	}
	for _, c := range cases {
		d := Decide(Parse([]byte(updates[c.kind])), c.reason, "Идут техработы", nil)
		if d.Action != c.want {
			t.Errorf("kind %d reason %d: action %d, want %d", c.kind, c.reason, d.Action, c.want)
			continue
		}
		if c.method == "" {
			continue
		}
		var m map[string]any
		json.Unmarshal(d.Body, &m)
		if m["method"] != c.method {
			t.Errorf("kind %d reason %d: method %v", c.kind, c.reason, m["method"])
		}
		if c.kind == KindCallback && m["show_alert"] != c.alert {
			t.Errorf("reason %d: show_alert %v", c.reason, m["show_alert"])
		}
		if c.kind == KindPreCheckout && m["ok"] != false {
			t.Error("pre_checkout must be refused")
		}
	}
}

func TestOneMessagePerMinute(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.Now = func() time.Time { return now }
	i := Parse([]byte(updates[KindPrivateMessage]))
	if Decide(i, Maintenance, "x", l).Action != Reply {
		t.Fatal("first message")
	}
	if Decide(i, Maintenance, "x", l).Action != Drop {
		t.Fatal("second message within a minute must be silent")
	}
	now = now.Add(61 * time.Second)
	if Decide(i, Maintenance, "x", l).Action != Reply {
		t.Fatal("after a minute")
	}
	if Decide(i, Antiflood, "", l).Action != Drop {
		t.Fatal("empty text: no reply")
	}
}
