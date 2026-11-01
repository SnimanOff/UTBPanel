package edge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/github"
)

// GitHubHook is a webhook of GitHub for a bot, checked by the core.
type GitHubHook struct {
	BotID     int64  `cbor:"1,keyasint"`
	Event     string `cbor:"2,keyasint"`
	Signature string `cbor:"3,keyasint"`
	Body      []byte `cbor:"4,keyasint"`
}

// MethodGitHubHook is the core method for /gh/<id>.
const MethodGitHubHook = "github.webhook"

// ghNets are GitHub's hook addresses from api.github.com/meta, refreshed
// daily and kept on disk for restarts without the network.
type ghNets struct {
	mu   sync.RWMutex
	path string
	nets []netip.Prefix
}

func (g *ghNets) load() {
	b, err := os.ReadFile(g.path)
	if err != nil {
		return
	}
	var ss []string
	if json.Unmarshal(b, &ss) != nil {
		return
	}
	g.set(ss)
}

func (g *ghNets) set(ss []string) {
	var nets []netip.Prefix
	for _, s := range ss {
		if p, err := netip.ParsePrefix(s); err == nil {
			nets = append(nets, p)
		}
	}
	g.mu.Lock()
	g.nets = nets
	g.mu.Unlock()
}

func (g *ghNets) contains(ip netip.Addr) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, p := range g.nets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// refreshGitHub fetches the addresses now and then once a day.
func (e *Edge) refreshGitHub(ctx context.Context, gh *github.Client) {
	for {
		nets, err := gh.HookNets(ctx)
		next := 24 * time.Hour
		if err != nil {
			e.Log.Warn("github hook addresses not refreshed", "err", err)
			next = time.Hour
		} else {
			ss := make([]string, len(nets))
			for i, n := range nets {
				ss[i] = n.String()
			}
			e.github.set(ss)
			b, _ := json.Marshal(ss)
			os.WriteFile(e.github.path, b, 0o600)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
	}
}

func (e *Edge) initGitHub() {
	e.github.path = filepath.Join(e.Cfg.DataDir, "github-hooks.json")
	e.github.load()
}

// githubHook hands GitHub webhooks to the core, which checks the signature
// with the bot's secret and fetches the release.
func (e *Edge) githubHook(w http.ResponseWriter, r *http.Request, ip netip.Addr) {
	if !e.github.contains(ip) {
		e.count("rejected_ip")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	id, ok := botFromPath(r.URL.Path, "/gh/")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	c := e.Core.Conn()
	if c == nil {
		// GitHub shows the failure; the poller catches up within 5 minutes.
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var status int
	in := GitHubHook{BotID: id, Event: r.Header.Get("X-GitHub-Event"), Signature: r.Header.Get("X-Hub-Signature-256"), Body: body}
	if err := c.Call(ctx, MethodGitHubHook, in, &status); err != nil || status == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(status)
}
