// Package servicebot forwards notifications of the feed to the owner's
// Telegram through a service bot. The panel never holds the token: the core
// keeps it sealed and sends on the panel's request (servicebot.send).
package servicebot

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Levels of what is sent.
const (
	LevelErrors   = "errors"   // only errors and their recoveries
	LevelWarnings = "warnings" // errors and warnings
)

// PerMinute caps messages; the rest fold into one summary.
var PerMinute = 20

// State is the API view of the service bot.
type State struct {
	Configured bool   `json:"configured"`
	Username   string `json:"username"`
	Level      string `json:"level"`
	OwnerTgID  int64  `json:"owner_tg_id"`
}

type coreState struct {
	Configured bool   `cbor:"1,keyasint"`
	Username   string `cbor:"2,keyasint"`
}

type setReq struct {
	Token string        `cbor:"1,keyasint"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

type sendReq struct {
	ChatID int64  `cbor:"1,keyasint"`
	Text   string `cbor:"2,keyasint"`
}

// Service forwards notifications and serves the settings.
type Service struct {
	DB    *store.DB
	Core  *wire.Client
	Notes *alerts.Service
	Owner func(ctx context.Context) int64
	Log   *slog.Logger
	// WriteErr translates core errors (bots.WriteCoreError).
	WriteErr func(http.ResponseWriter, error)

	mu      sync.Mutex
	sent    map[string]time.Time // keys sent, for their recoveries
	window  time.Time
	inMin   int
	skipped int
}

// Level returns the stored level.
func (s *Service) Level(ctx context.Context) string {
	var v string
	if s.DB.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'service_bot_level'`).Scan(&v) != nil || v != LevelWarnings {
		return LevelErrors
	}
	return v
}

func (s *Service) setLevel(ctx context.Context, level string) error {
	_, err := s.DB.W.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('service_bot_level', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, level)
	return err
}

func (s *Service) state(ctx context.Context) (State, error) {
	var cs coreState
	if err := s.Core.Call(ctx, "servicebot.get", struct{}{}, &cs); err != nil {
		return State{}, err
	}
	return State{Configured: cs.Configured, Username: cs.Username, Level: s.Level(ctx), OwnerTgID: s.Owner(ctx)}, nil
}

// wanted decides whether a notification goes to Telegram.
func (s *Service) wanted(n alerts.Notification, level string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sent == nil {
		s.sent = map[string]time.Time{}
	}
	switch n.Severity {
	case "error":
	case "warn":
		if level != LevelWarnings {
			return false
		}
	case "ok":
		base, ok := strings.CutSuffix(n.Key, ":ok")
		if _, was := s.sent[base]; !ok || !was {
			return false
		}
		delete(s.sent, base)
		return true
	default:
		return false
	}
	if n.Key != "" {
		s.sent[n.Key] = time.Now()
	}
	return true
}

// allow applies PerMinute; a refused message is counted for the summary.
func (s *Service) allow(now time.Time) (ok bool, summary int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.window) >= time.Minute {
		s.window, s.inMin = now, 0
		summary, s.skipped = s.skipped, 0
	}
	if s.inMin >= PerMinute {
		s.skipped++
		return false, summary
	}
	s.inMin++
	return true, summary
}

func icon(sev string) string {
	switch sev {
	case "error":
		return "🔴 "
	case "warn":
		return "🟡 "
	case "ok":
		return "🟢 "
	}
	return ""
}

func (s *Service) send(ctx context.Context, text string) error {
	owner := s.Owner(ctx)
	if owner == 0 {
		return errors.New("owner Telegram ID is not set")
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return s.Core.Call(cctx, "servicebot.send", sendReq{ChatID: owner, Text: text}, nil)
}

// Run forwards new notifications until ctx ends.
func (s *Service) Run(ctx context.Context) {
	sub, cancel := s.Notes.Subscribe()
	defer cancel()
	queue := make(chan string, 64)
	go func() {
		for text := range queue {
			if err := s.send(ctx, text); err != nil && ctx.Err() == nil {
				var we *wire.Error
				if !errors.As(err, &we) || we.Code != wire.CodeNotFound {
					s.Log.Warn("service bot message not sent", "err", err)
				}
			}
		}
	}()
	defer close(queue)
	flush := time.NewTicker(time.Minute)
	defer flush.Stop()
	push := func(text string) {
		select {
		case queue <- text:
		default:
			s.mu.Lock()
			s.skipped++
			s.mu.Unlock()
		}
	}
	summary := func(n int) {
		if n > 0 {
			push("… и ещё " + strconv.Itoa(n) + " уведомлений, подробности в панели")
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-flush.C:
			_, n := s.allow(time.Now())
			summary(n)
			s.prune()
		case n := <-sub:
			if !s.wanted(n, s.Level(ctx)) {
				continue
			}
			ok, folded := s.allow(time.Now())
			summary(folded)
			if ok {
				push(icon(n.Severity) + n.Text)
			}
		}
	}
}

func (s *Service) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.sent {
		if time.Since(t) > 7*24*time.Hour {
			delete(s.sent, k)
		}
	}
}

// Register adds /api/server/service-bot.
func (s *Service) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/server/service-bot", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.state(r.Context())
		if err != nil {
			s.WriteErr(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("PUT /api/server/service-bot", func(w http.ResponseWriter, r *http.Request) {
		if !api.CallerFrom(r.Context()).IsOwner() {
			api.WriteError(w, http.StatusForbidden, "owner_only", "only the owner key can change the service bot")
			return
		}
		var in struct {
			Token string `json:"token"`
			Level string `json:"level"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&in); err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if in.Level != "" && in.Level != LevelErrors && in.Level != LevelWarnings {
			api.WriteError(w, http.StatusBadRequest, "bad_request", "level must be errors or warnings")
			return
		}
		details := map[string]any{}
		if in.Token != "" {
			if err := s.Core.Call(r.Context(), "servicebot.set", setReq{Token: in.Token, Actor: api.Actor(r.Context())}, nil); err != nil {
				s.WriteErr(w, err)
				return
			}
			details["token"] = "изменено"
		}
		if in.Level != "" {
			if err := s.setLevel(r.Context(), in.Level); err != nil {
				api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
				return
			}
			details["level"] = in.Level
		}
		audit.Annotate(r, "service_bot.set", "", details)
		st, err := s.state(r.Context())
		if err != nil {
			s.WriteErr(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("DELETE /api/server/service-bot", func(w http.ResponseWriter, r *http.Request) {
		if !api.CallerFrom(r.Context()).IsOwner() {
			api.WriteError(w, http.StatusForbidden, "owner_only", "only the owner key can change the service bot")
			return
		}
		if err := s.Core.Call(r.Context(), "servicebot.delete", api.Actor(r.Context()), nil); err != nil {
			s.WriteErr(w, err)
			return
		}
		audit.Annotate(r, "service_bot.delete", "", nil)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/server/service-bot/test", func(w http.ResponseWriter, r *http.Request) {
		if s.Owner(r.Context()) == 0 {
			api.WriteError(w, http.StatusConflict, "no_owner_id", "set the owner's Telegram ID in the panel settings first")
			return
		}
		if err := s.send(r.Context(), "🟢 Проверка: сервисный бот UTBPanel подключён, уведомления будут приходить сюда."); err != nil {
			var we *wire.Error
			if errors.As(err, &we) && we.Code == "telegram" && strings.HasPrefix(we.Message, "403") {
				api.WriteError(w, http.StatusConflict, "not_started", "откройте сервисного бота в Telegram и нажмите Start, затем проверьте ещё раз")
				return
			}
			s.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
