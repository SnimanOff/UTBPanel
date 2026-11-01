package bots

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/broadcast"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

const maxPhoto = 10 << 20 // Telegram's limit for sendPhoto uploads

func (s *Service) registerBroadcasts(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/broadcasts", s.listBroadcasts)
	mux.HandleFunc("POST /api/bots/{id}/broadcasts", s.createBroadcast)
	mux.HandleFunc("POST /api/bots/{id}/broadcasts/test", s.testBroadcast)
	mux.HandleFunc("GET /api/bots/{id}/broadcasts/estimate", s.estimateBroadcast)
	mux.HandleFunc("GET /api/bots/{id}/broadcasts/{bid}", s.getBroadcast)
	for _, op := range []string{"pause", "resume", "cancel"} {
		mux.HandleFunc("POST /api/bots/{id}/broadcasts/{bid}/"+op, s.controlBroadcast("broadcast."+op))
	}
}

// readBroadcast takes JSON, or multipart with "data" (JSON) and "photo".
func readBroadcast(w http.ResponseWriter, r *http.Request, b *broadcast.B) bool {
	var in struct {
		Text       string             `json:"text"`
		ParseMode  string             `json:"parse_mode"`
		Buttons    []broadcast.Button `json:"buttons"`
		Audience   string             `json:"audience"`
		ActiveDays int                `json:"active_days"`
		StartsAt   *string            `json:"starts_at"`
	}
	bad := func(msg string) bool {
		api.WriteError(w, http.StatusBadRequest, "bad_request", msg)
		return false
	}
	if mt := r.Header.Get("Content-Type"); len(mt) >= 19 && mt[:19] == "multipart/form-data" {
		if err := r.ParseMultipartForm(maxPhoto + 1<<20); err != nil {
			return bad(err.Error())
		}
		if err := json.Unmarshal([]byte(r.FormValue("data")), &in); err != nil {
			return bad("data: " + err.Error())
		}
		if f, h, err := r.FormFile("photo"); err == nil {
			defer f.Close()
			if h.Size > maxPhoto {
				return bad("картинка больше 10 МБ")
			}
			b.Photo, _ = io.ReadAll(io.LimitReader(f, maxPhoto))
			b.PhotoName = h.Filename
		}
	} else if !decode(w, r, &in) {
		return false
	}
	switch in.ParseMode {
	case "", "HTML", "MarkdownV2":
	default:
		return bad("parse_mode: HTML или MarkdownV2")
	}
	for _, bt := range in.Buttons {
		if bt.Text == "" || len(bt.URL) < 8 || (bt.URL[:7] != "http://" && bt.URL[:8] != "https://" && bt.URL[:5] != "tg://") {
			return bad("кнопка: нужен текст и ссылка http(s):// или tg://")
		}
	}
	b.Text, b.ParseMode, b.Buttons, b.Audience, b.ActiveDays = in.Text, in.ParseMode, in.Buttons, in.Audience, in.ActiveDays
	if in.StartsAt != nil && *in.StartsAt != "" {
		t, err := time.Parse(time.RFC3339, *in.StartsAt)
		if err != nil {
			return bad("starts_at: " + err.Error())
		}
		b.StartsAt = t
	}
	return true
}

func (s *Service) createBroadcast(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b := broadcast.B{BotID: id}
	if !readBroadcast(w, r, &b) {
		return
	}
	var out broadcast.B
	if err := s.Call(r.Context(), "broadcast.create", core.BroadcastReq{B: b, Actor: api.Actor(r.Context())}, &out); err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "broadcast.create", strconv.FormatInt(out.ID, 10), map[string]any{"audience": b.Audience, "starts_at": out.StartsAt})
	api.WriteJSON(w, http.StatusCreated, out)
}

// testBroadcast sends the message to the owner's Telegram ID.
func (s *Service) testBroadcast(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b := broadcast.B{BotID: id}
	if !readBroadcast(w, r, &b) {
		return
	}
	var chat int64
	if s.Owner != nil {
		chat = s.Owner(r)
	}
	if chat == 0 {
		api.WriteError(w, http.StatusBadRequest, "no_owner_id", "укажите свой Telegram ID в настройках панели")
		return
	}
	if err := s.Call(r.Context(), "broadcast.test", core.BroadcastReq{B: b, TestChat: chat, Actor: api.Actor(r.Context())}, nil); err != nil {
		WriteCoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) listBroadcasts(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var out []broadcast.B
	if err := s.Call(r.Context(), "broadcast.list", core.BroadcastReq{B: broadcast.B{BotID: id}}, &out); err != nil {
		WriteCoreError(w, err)
		return
	}
	if out == nil {
		out = []broadcast.B{}
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) estimateBroadcast(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	var n int
	req := core.BroadcastReq{B: broadcast.B{BotID: id, Audience: r.URL.Query().Get("audience"), ActiveDays: days}}
	if err := s.Call(r.Context(), "broadcast.estimate", req, &n); err != nil {
		WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]int{"count": n})
}

func broadcastID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	bid, err := strconv.ParseInt(r.PathValue("bid"), 10, 64)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "bad_request", "bad broadcast id")
		return 0, false
	}
	return bid, true
}

func (s *Service) getBroadcast(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	bid, ok := broadcastID(w, r)
	if !ok {
		return
	}
	var out broadcast.B
	if err := s.Call(r.Context(), "broadcast.get", core.BroadcastReq{B: broadcast.B{BotID: id}, ID: bid}, &out); err != nil {
		WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) controlBroadcast(method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		bid, ok := broadcastID(w, r)
		if !ok {
			return
		}
		req := core.BroadcastReq{B: broadcast.B{BotID: id}, ID: bid, Actor: api.Actor(r.Context())}
		if err := s.Call(r.Context(), method, req, nil); err != nil {
			WriteCoreError(w, err)
			return
		}
		audit.Annotate(r, method, strconv.FormatInt(bid, 10), nil)
		w.WriteHeader(http.StatusNoContent)
	}
}
