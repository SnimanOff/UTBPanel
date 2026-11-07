package core

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/logs"
)

func TestIntercept(t *testing.T) {
	c := newCore(t)
	call := func(m string) ([]byte, int, bool) {
		return c.intercept(httptest.NewRequest("POST", "/bot1:x/"+m, nil), 1, "main", m)
	}
	for m, want := range map[string]int{"setWebhook": 200, "deleteWebhook": 200, "getUpdates": 409} {
		body, status, ok := call(m)
		if !ok || status != want {
			t.Errorf("%s: %d %v %s", m, status, ok, body)
		}
	}
	if _, _, ok := call("getWebhookInfo"); ok {
		t.Error("getWebhookInfo must pass through")
	}
	if _, _, ok := call("sendMessage"); ok {
		t.Error("sendMessage must pass through")
	}
	lines := c.Logs.Tail(1, 10, logs.Filter{Stream: "core"})
	all := ""
	for _, l := range lines {
		all += l.Text + " | "
	}
	if len(lines) != 3 || !strings.Contains(all, "getUpdates: refused") {
		t.Fatalf("log = %+v", lines)
	}
}
