package gate

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionsExpire(t *testing.T) {
	now := time.Now()
	g := &Gate{sessions: map[string]*session{
		"fresh": {created: now.Add(-time.Hour), seen: now.Add(-time.Minute)},
		"idle":  {created: now.Add(-14 * time.Hour), seen: now.Add(-13 * time.Hour)},
		"old":   {created: now.Add(-8 * 24 * time.Hour), seen: now.Add(-time.Minute)},
	}}
	with := func(sid string) authKind {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: CookieName, Value: sid})
		return g.auth(r)
	}
	if with("fresh") != authSession {
		t.Fatal("a fresh session was refused")
	}
	if g.sessions["fresh"].seen.Before(now) {
		t.Fatal("activity does not extend the session")
	}
	for _, sid := range []string{"idle", "old", "unknown"} {
		if with(sid) != authNone {
			t.Errorf("%s session accepted", sid)
		}
		if _, ok := g.sessions[sid]; ok {
			t.Errorf("%s session kept", sid)
		}
	}
}
