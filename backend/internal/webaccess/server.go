// Package webaccess is the second door of the panel: a TLS listener with
// a Let's Encrypt certificate where a phone signs in with a passkey. The
// owner invites a person, compares the four-word code and approves. A
// person may see all bots (the owner's phone) or only some, with the role
// view or manage; operations are allowed by their x-mcp level.
package webaccess

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	spec "github.com/SnimanOff/UTBPanel/backend/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
)

// CookieName holds the session; __Host- pins it to this origin.
const CookieName = "__Host-utb"

// KindWeb is the identity kind of passkey callers.
const KindWeb pki.Kind = "web"

// Service serves the passkey listener and the owner's "Access" API.
type Service struct {
	Store  *Store
	RP     RP
	Listen string
	API    *api.Server
	Static http.Handler
	// Cert fetches the domain certificate from the edge.
	Cert func(ctx context.Context) (*tls.Certificate, error)
	// Pending is told about a passkey waiting for approval.
	Pending func(ctx context.Context, name, code, ip string)
	Log     *slog.Logger

	ops   map[string]string
	mu    sync.Mutex
	chals map[string]challenge
	rate  map[string]*window
	cert  *tls.Certificate
	certT time.Time
}

type challenge struct {
	value   []byte
	expires time.Time
}

type window struct {
	start time.Time
	n     int
}

// New prepares the service; domain empty means the listener is off.
func New(st *Store, domain, listen string, server *api.Server, static http.Handler, log *slog.Logger) (*Service, error) {
	ops, err := spec.Operations()
	if err != nil {
		return nil, err
	}
	s := &Service{Store: st, Listen: listen, API: server, Static: static, Log: log,
		ops: map[string]string{}, chals: map[string]challenge{}, rate: map[string]*window{}}
	for _, op := range ops {
		s.ops[op.Pattern()] = op.MCP
	}
	if domain != "" {
		_, port, _ := net.SplitHostPort(listen)
		origin := "https://" + domain
		if port != "" && port != "443" {
			origin += ":" + port
		}
		s.RP = RP{ID: domain, Origin: origin}
	}
	return s, nil
}

// Enabled reports whether a domain is set.
func (s *Service) Enabled() bool { return s.RP.ID != "" }

func (s *Service) newChallenge(key string) []byte {
	c := random(32)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.chals {
		if now.After(v.expires) {
			delete(s.chals, k)
		}
	}
	s.chals[key] = challenge{value: c, expires: now.Add(3 * time.Minute)}
	return c
}

func (s *Service) takeChallenge(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chals[key]
	delete(s.chals, key)
	if !ok || time.Now().After(c.expires) {
		return nil, false
	}
	return c.value, true
}

// limited allows 30 sign-in requests a minute per IP.
func (s *Service) limited(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.rate[ip]
	now := time.Now()
	if w == nil || now.Sub(w.start) > time.Minute {
		if len(s.rate) > 10000 {
			s.rate = map[string]*window{}
		}
		w = &window{start: now}
		s.rate[ip] = w
	}
	w.n++
	return w.n > 30
}

// getCertificate serves the edge's certificate, refreshed every 12 hours.
func (s *Service) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	cert, at := s.cert, s.certT
	s.mu.Unlock()
	if cert != nil && time.Since(at) < 12*time.Hour {
		return cert, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fresh, err := s.Cert(ctx)
	if err != nil {
		if cert != nil {
			return cert, nil
		}
		return nil, err
	}
	s.mu.Lock()
	s.cert, s.certT = fresh, time.Now()
	s.mu.Unlock()
	return fresh, nil
}

// Run serves the listener until ctx ends.
func (s *Service) Run(ctx context.Context) error {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: s.getCertificate, NextProtos: []string{"h2", "http/1.1"}}
	ln, err := tls.Listen("tcp", s.Listen, cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       5 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.Log.Handler(), slog.LevelDebug),
	}
	//nolint:gosec // the shutdown must outlive ctx, which is already done
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx) // the listener is closing anyway
	}()
	s.Log.Info("passkey listener started", "addr", s.Listen, "origin", s.RP.Origin)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func clientIP(r *http.Request) string {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

// csrf: changing requests need JSON, X-Panel: 1 and our own Origin.
func (s *Service) csrf(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return true
	case http.MethodOptions:
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	if r.Header.Get("X-Panel") != "1" {
		api.WriteError(w, http.StatusForbidden, "csrf", "missing X-Panel: 1 header")
		return false
	}
	if o := r.Header.Get("Origin"); o != s.RP.Origin {
		api.WriteError(w, http.StatusForbidden, "csrf", "foreign Origin")
		return false
	}
	if r.ContentLength != 0 || r.Header.Get("Transfer-Encoding") != "" {
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt != "application/json" && mt != "multipart/form-data" {
			api.WriteError(w, http.StatusUnsupportedMediaType, "csrf", "body must be application/json or multipart/form-data")
			return false
		}
	}
	return true
}

func (s *Service) host(r *http.Request) bool {
	h, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		h = r.Host
	}
	return strings.EqualFold(h, s.RP.ID)
}

// Handler is the whole passkey listener.
func (s *Service) Handler() http.Handler {
	chain := s.API.Chain()
	web := http.NewServeMux()
	web.HandleFunc("GET /web/invite/{token}", s.inviteInfo)
	web.HandleFunc("POST /web/invite/{token}/begin", s.inviteBegin)
	web.HandleFunc("POST /web/invite/{token}/finish", s.inviteFinish)
	web.HandleFunc("GET /web/pending/{poll}", s.pending)
	web.HandleFunc("POST /web/login/begin", s.loginBegin)
	web.HandleFunc("POST /web/login/finish", s.loginFinish)
	web.HandleFunc("POST /web/logout", s.logout)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		api.NoFraming(h)
		h.Set("Strict-Transport-Security", "max-age=31536000")
		if !s.host(r) {
			api.WriteError(w, http.StatusMisdirectedRequest, "bad_host", "open the panel by its domain")
			return
		}
		if !s.csrf(w, r) {
			return
		}
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/web/"):
			if r.Method == http.MethodPost && s.limited(clientIP(r)) {
				api.WriteError(w, http.StatusTooManyRequests, "rate_limited", "слишком много попыток, подождите минуту")
				return
			}
			web.ServeHTTP(w, r)
		case p == "/api" || strings.HasPrefix(p, "/api/"):
			c, ok := s.caller(w, r)
			if !ok {
				return
			}
			if err := s.allow(c, r); err != nil {
				api.WriteError(w, http.StatusForbidden, "forbidden", err.Error())
				return
			}
			chain.ServeHTTP(w, r.WithContext(api.WithCaller(r.Context(), c)))
		case p == "/mcp" || strings.HasPrefix(p, "/enroll") || p == "/llms.txt" || strings.HasPrefix(p, "/docs/"):
			http.NotFound(w, r)
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			s.Static.ServeHTTP(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// caller authenticates the session cookie.
func (s *Service) caller(w http.ResponseWriter, r *http.Request) (api.Caller, bool) {
	ck, err := r.Cookie(CookieName)
	if err != nil {
		api.WriteError(w, http.StatusUnauthorized, "login_required", "войдите по passkey")
		return api.Caller{}, false
	}
	a, cred, err := s.Store.Lookup(r.Context(), ck.Value)
	if errors.Is(err, ErrAccessEnded) {
		api.WriteError(w, http.StatusUnauthorized, "login_required", "срок доступа истёк: попросите владельца продлить его")
		return api.Caller{}, false
	}
	if err != nil {
		api.WriteError(w, http.StatusUnauthorized, "login_required", "сессия закончилась, войдите снова")
		return api.Caller{}, false
	}
	scope := &api.Scope{All: a.AllBots, Role: a.Role, Bots: map[int64]bool{}}
	for _, id := range a.Bots {
		scope.Bots[id] = true
	}
	id := b64.EncodeToString(cred)
	if len(id) > 10 {
		id = id[:10]
	}
	return api.Caller{
		Identity: pki.Identity{Serial: "web:" + id, Name: "web:" + a.Name, Kind: KindWeb},
		IP:       clientIP(r), Source: api.SourceWeb, Scope: scope,
	}, true
}

// noBot are the routes without a bot that a limited person may use.
var noBot = map[string]bool{"GET /api/ping": true, "GET /api/bots": true, "GET /api/live": true}

// allow checks a limited person's request by the route and its x-mcp level.
func (s *Service) allow(c api.Caller, r *http.Request) error {
	if !c.Limited() {
		return nil
	}
	_, pattern := s.API.Mux.Handler(r)
	if pattern == "" {
		return nil // 404 from the mux
	}
	if noBot[pattern] {
		return nil
	}
	_, path, _ := strings.Cut(pattern, " ")
	if !strings.HasPrefix(path, "/api/bots/{") {
		return errors.New("доступно только владельцу")
	}
	parts := strings.Split(r.URL.Path, "/") // "", api, bots, <id>, ...
	id, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || !c.SeesBot(id) {
		return errors.New("нет доступа к этому боту")
	}
	switch level := s.ops[pattern]; {
	case level == spec.Read:
		return nil
	case (level == spec.Write || level == spec.Confirm) && c.Scope.Role == RoleManage:
		return nil
	case level == spec.Write || level == spec.Confirm:
		return errors.New("роль «Просмотр» не меняет настройки")
	default:
		return errors.New("эта операция доступна только владельцу")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return false
	}
	return true
}

func (s *Service) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", MaxAge: int(SessionTTL.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

var roleNames = map[string]string{RoleView: "просмотр", RoleManage: "управление", RoleFull: "все права"}

func (s *Service) inviteInfo(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.InviteAccess(r.Context(), r.PathValue("token"))
	if err != nil {
		api.WriteError(w, http.StatusGone, "invite_expired", "ссылка устарела или уже использована: попросите новую")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"name": a.Name, "role": a.Role, "role_name": roleNames[a.Role], "all_bots": a.AllBots, "bots": len(a.Bots)})
}

func (s *Service) inviteBegin(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	a, err := s.Store.InviteAccess(r.Context(), token)
	if err != nil {
		api.WriteError(w, http.StatusGone, "invite_expired", "ссылка устарела или уже использована: попросите новую")
		return
	}
	c := s.newChallenge("inv:" + string(hash(token)))
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"challenge": b64.EncodeToString(c),
		"rp":        map[string]string{"id": s.RP.ID, "name": "UTBPanel"},
		"user":      map[string]string{"id": b64.EncodeToString(random(16)), "name": a.Name, "displayName": a.Name},
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": algES256}, {"type": "public-key", "alg": algRS256},
		},
		"authenticatorSelection": map[string]any{"residentKey": "required", "requireResidentKey": true, "userVerification": "required"},
		"attestation":            "none",
		"timeout":                120000,
	})
}

func (s *Service) inviteFinish(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	var in Registration
	if !decode(w, r, &in) {
		return
	}
	a, err := s.Store.InviteAccess(r.Context(), token)
	if err != nil {
		api.WriteError(w, http.StatusGone, "invite_expired", "ссылка устарела или уже использована: попросите новую")
		return
	}
	chal, ok := s.takeChallenge("inv:" + string(hash(token)))
	if !ok {
		api.WriteError(w, http.StatusBadRequest, "challenge_expired", "попробуйте ещё раз")
		return
	}
	cred, err := s.RP.VerifyRegistration(in, chal)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "bad_passkey", err.Error())
		return
	}
	pub, err := x509.ParsePKIXPublicKey(cred.PublicKey)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "bad_passkey", err.Error())
		return
	}
	code := pki.Code(pub)
	device := r.UserAgent()
	if len(device) > 120 {
		device = device[:120]
	}
	poll, err := s.Store.Enroll(r.Context(), token, a.ID, cred, code, device, clientIP(r))
	if errors.Is(err, ErrExpired) {
		api.WriteError(w, http.StatusGone, "invite_expired", "ссылка уже использована")
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if s.Pending != nil {
		s.Pending(r.Context(), a.Name, code, clientIP(r))
	}
	api.WriteJSON(w, http.StatusOK, map[string]string{"code": code, "poll": poll})
}

func (s *Service) pending(w http.ResponseWriter, r *http.Request) {
	state, cred, err := s.Store.Poll(r.Context(), r.PathValue("poll"))
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if state == "approved" {
		if a, err := s.Store.Get(r.Context(), cred.AccessID); err == nil && a.ExpiredAt(time.Now()) {
			api.WriteError(w, http.StatusForbidden, "access_expired", "срок доступа истёк: попросите владельца продлить его")
			return
		}
		token, err := s.Store.Session(r.Context(), cred.ID, cred.AccessID, clientIP(r), 0)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		s.setSession(w, token)
	}
	api.WriteJSON(w, http.StatusOK, map[string]string{"state": state})
}

func (s *Service) loginBegin(w http.ResponseWriter, r *http.Request) {
	id := b64.EncodeToString(random(16))
	c := s.newChallenge("login:" + id)
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"challenge_id": id,
		"public_key": map[string]any{
			"challenge": b64.EncodeToString(c), "rpId": s.RP.ID, "userVerification": "required", "timeout": 120000,
		},
	})
}

func (s *Service) loginFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ChallengeID string `json:"challenge_id"`
		Assertion
	}
	if !decode(w, r, &in) {
		return
	}
	chal, ok := s.takeChallenge("login:" + in.ChallengeID)
	if !ok {
		api.WriteError(w, http.StatusBadRequest, "challenge_expired", "попробуйте ещё раз")
		return
	}
	fail := func() {
		api.WriteError(w, http.StatusUnauthorized, "bad_passkey", "этот ключ входа не подходит или отозван")
	}
	credID, err := b64.DecodeString(in.ID)
	if err != nil {
		fail()
		return
	}
	cred, err := s.Store.credential(r.Context(), credID)
	if err != nil {
		fail()
		return
	}
	if !cred.Approved {
		api.WriteError(w, http.StatusForbidden, "not_approved", "владелец ещё не подтвердил этот ключ входа")
		return
	}
	if a, err := s.Store.Get(r.Context(), cred.AccessID); err == nil && a.ExpiredAt(time.Now()) {
		api.WriteError(w, http.StatusForbidden, "access_expired", "срок доступа истёк: попросите владельца продлить его")
		return
	}
	count, err := s.RP.VerifyAssertion(in.Assertion, chal, cred.PublicKey, cred.Alg, cred.Count)
	if err != nil {
		s.Log.Warn("passkey sign-in refused", "ip", clientIP(r), "err", err)
		fail()
		return
	}
	token, err := s.Store.Session(r.Context(), cred.ID, cred.AccessID, clientIP(r), count)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.setSession(w, token)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(CookieName); err == nil {
		s.Store.EndSession(r.Context(), ck.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}
