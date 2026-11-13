// Package bots is the panel side of bot management: it calls the core for
// everything about processes, tokens and files, and keeps groups and order
// in panel.db.
package bots

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/logs"
	"github.com/SnimanOff/UTBPanel/backend/internal/maintenance"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Service serves the bot API of the panel.
type Service struct {
	Core   *wire.Client
	Groups *Groups
	// Owner is the owner's Telegram ID from the panel settings (previews).
	Owner func(*http.Request) int64
	// OnSecretSet closes requests of an AI waiting for this secret.
	OnSecretSet func(ctx context.Context, botID int64, name, by string)
}

// Call calls the core.
func (s *Service) Call(ctx context.Context, method string, in, out any) error {
	return s.Core.Call(ctx, method, in, out)
}

// WriteCoreError translates a core error into the API format: Telegram's
// answer is passed as is, under "telegram".
func WriteCoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, wire.ErrNotConnected) || errors.Is(err, wire.ErrClosed) {
		api.WriteError(w, http.StatusServiceUnavailable, "core_unavailable", "the core is not running")
		return
	}
	var we *wire.Error
	if !errors.As(err, &we) {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	status := http.StatusBadRequest
	switch we.Code {
	case wire.CodeNotFound:
		status = http.StatusNotFound
	case "exists", "conflict":
		status = http.StatusConflict
	case core.CodeTelegram:
		status = http.StatusUnprocessableEntity
		var raw string
		wire.Unmarshal(we.Data, &raw)
		body := api.ErrorBody{Code: we.Code, Message: we.Message}
		if json.Valid([]byte(raw)) {
			body.Telegram = json.RawMessage(raw)
		}
		api.WriteJSON(w, status, body)
		return
	case wire.CodeInternal:
		status = http.StatusInternalServerError
	case wire.CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	api.WriteError(w, status, we.Code, we.Message)
}

// View is a bot as the interface shows it.
type View struct {
	core.Bot
	GroupID  int64         `json:"group_id,omitempty"`
	Position int           `json:"position"`
	Status   runner.Status `json:"status"`
}

// Register adds the routes.
func (s *Service) Register(mux *api.Mux) {
	s.registerSDKMetrics(mux)
	s.registerSDKTasks(mux)
	s.registerSDKFlags(mux)
	mux.HandleFunc("GET /api/bots", s.list)
	mux.HandleFunc("POST /api/bots", s.create)
	mux.HandleFunc("GET /api/bots/{id}", s.get)
	mux.HandleFunc("PATCH /api/bots/{id}", s.rename)
	mux.HandleFunc("DELETE /api/bots/{id}", s.delete)
	mux.HandleFunc("PUT /api/bots/{id}/token", s.setToken)
	mux.HandleFunc("PUT /api/bots/{id}/group", s.setGroup)
	mux.HandleFunc("POST /api/bots/{id}/start", s.lifecycle("bots.start"))
	mux.HandleFunc("POST /api/bots/{id}/stop", s.lifecycle("bots.stop"))
	mux.HandleFunc("POST /api/bots/{id}/restart", s.lifecycle("bots.restart"))
	mux.HandleFunc("POST /api/bots/{id}/pause", s.maint("maintenance.pause", "bot.pause"))
	mux.HandleFunc("POST /api/bots/{id}/resume", s.maint("maintenance.end", "bot.resume"))
	mux.HandleFunc("GET /api/bots/{id}/maintenance", s.getMaint)
	mux.HandleFunc("GET /api/bots/{id}/profile", s.getProfile)
	s.registerBroadcasts(mux)
	s.registerCanary(mux)
	s.registerTrial(mux)
	mux.HandleFunc("PATCH /api/bots/{id}/profile", s.setProfile)
	mux.HandleFunc("POST /api/bots/{id}/maintenance", s.schedule)
	mux.HandleFunc("POST /api/bots/{id}/maintenance/extend", s.extend)
	mux.HandleFunc("GET /api/bots/{id}/settings", s.settings)
	mux.HandleFunc("PATCH /api/bots/{id}/settings", s.updateSettings)
	mux.HandleFunc("PUT /api/bots/{id}/settings/{section}", s.putSection)
	mux.HandleFunc("GET /api/bots/{id}/secrets", s.listSecrets)
	mux.HandleFunc("PUT /api/bots/{id}/secrets/{name}", s.setSecret)
	mux.HandleFunc("DELETE /api/bots/{id}/secrets/{name}", s.deleteSecret)
	mux.HandleFunc("GET /api/bots/{id}/logs", s.logs)
	mux.HandleFunc("GET /api/bots/{id}/logs/stream", s.logStream)
	mux.HandleFunc("GET /api/bots/{id}/logs/download", s.logDownload)
	mux.HandleFunc("PUT /api/bots/order", s.order)
	mux.HandleFunc("GET /api/groups", s.groups)
	mux.HandleFunc("POST /api/groups", s.createGroup)
	mux.HandleFunc("PATCH /api/groups/{gid}", s.renameGroup)
	mux.HandleFunc("DELETE /api/groups/{gid}", s.deleteGroup)
	mux.HandleFunc("PUT /api/groups/order", s.orderGroups)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return false
	}
	return true
}

func (s *Service) view(ctx context.Context, b core.Bot, metas map[int64]Meta) View {
	v := View{Bot: b}
	if m, ok := metas[b.ID]; ok {
		v.GroupID, v.Position = m.GroupID, m.Position
	}
	s.Call(ctx, "bots.status", b.ID, &v.Status)
	return v
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	var bs []core.Bot
	if err := s.Call(r.Context(), "bots.list", struct{}{}, &bs); err != nil {
		WriteCoreError(w, err)
		return
	}
	metas, err := s.Groups.Metas(r.Context())
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	caller := api.CallerFrom(r.Context())
	out := make([]View, 0, len(bs))
	for _, b := range bs {
		if !caller.SeesBot(b.ID) {
			continue
		}
		out = append(out, s.view(r.Context(), b, metas))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b core.Bot
	if err := s.Call(r.Context(), "bots.get", id, &b); err != nil {
		WriteCoreError(w, err)
		return
	}
	metas, _ := s.Groups.Metas(r.Context())
	api.WriteJSON(w, http.StatusOK, s.view(r.Context(), b, metas))
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	ctx, unlock, err := s.Groups.Guard.Lock(r.Context())
	if err != nil {
		WriteCoreError(w, err)
		return
	}
	defer unlock()
	r = r.WithContext(ctx)
	var in struct {
		Name    string `json:"name"`
		Token   string `json:"token"`
		GroupID int64  `json:"group_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	b, err := s.CreateBot(r.Context(), in.Name, in.Token, in.GroupID)
	if err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "bot.create", b.Name, map[string]any{"username": b.Username, "token": "изменено"})
	audit.SetBot(r, b.ID)
	// The token is never returned: only @username is shown from now on.
	api.WriteJSON(w, http.StatusCreated, b)
}

// CreateBot adds a bot (the core checks the token with getMe) into a group.
func (s *Service) CreateBot(ctx context.Context, name, token string, groupID int64) (core.Bot, error) {
	var b core.Bot
	if err := s.Call(ctx, "bots.create", core.CreateBot{Name: name, Token: token, Actor: api.Actor(ctx)}, &b); err != nil {
		return b, err
	}
	s.Groups.SetGroup(ctx, b.ID, groupID)
	return b, nil
}

func (s *Service) setToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return
	}
	var b core.Bot
	if err := s.Call(r.Context(), "bots.set_token", core.SetToken{BotID: id, Token: in.Token, Actor: api.Actor(r.Context())}, &b); err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "bot.token", b.Username, map[string]any{"token": "изменено"})
	api.WriteJSON(w, http.StatusOK, b)
}

func (s *Service) rename(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Call(r.Context(), "bots.rename", core.Rename{BotID: id, Name: in.Name, Actor: api.Actor(r.Context())}, nil); err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "bot.rename", in.Name, nil)
	api.WriteJSON(w, http.StatusOK, map[string]string{"name": in.Name})
}

func (s *Service) delete(w http.ResponseWriter, r *http.Request) {
	ctx, unlock, err := s.Groups.Guard.Lock(r.Context())
	if err != nil {
		WriteCoreError(w, err)
		return
	}
	defer unlock()
	r = r.WithContext(ctx)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Call(r.Context(), "bots.delete", core.BotRef{BotID: id, Actor: api.Actor(r.Context())}, nil); err != nil {
		WriteCoreError(w, err)
		return
	}
	s.Groups.Forget(r.Context(), id)
	audit.Annotate(r, "bot.delete", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) lifecycle(method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		if err := s.Call(ctx, method, core.BotRef{BotID: id, Actor: api.Actor(r.Context())}, nil); err != nil {
			WriteCoreError(w, err)
			return
		}
		audit.Annotate(r, "bot."+method[len("bots."):], "", nil)
		var st runner.Status
		s.Call(r.Context(), "bots.status", id, &st)
		api.WriteJSON(w, http.StatusOK, st)
	}
}

func (s *Service) setGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		GroupID int64 `json:"group_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Groups.SetGroup(r.Context(), id, in.GroupID); err != nil {
		api.WriteError(w, http.StatusNotFound, "not_found", "no such group")
		return
	}
	audit.Annotate(r, "bot.group", strconv.FormatInt(in.GroupID, 10), nil)
	api.WriteJSON(w, http.StatusOK, in)
}

func (s *Service) settings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var m map[string]json.RawMessage
	if err := s.Call(r.Context(), "bots.settings", id, &m); err != nil {
		WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, m)
}

func (s *Service) updateSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Env    *map[string]string `json:"env"`
		Rename *map[string]string `json:"rename"`
		Limits *runner.Limits     `json:"limits"`
	}
	if !decode(w, r, &in) {
		return
	}
	var before map[string]json.RawMessage
	s.Call(r.Context(), "bots.settings", id, &before)
	up := core.SettingsUpdate{BotID: id, Env: in.Env, Rename: in.Rename, Limits: in.Limits, Actor: api.Actor(r.Context())}
	if err := s.Call(r.Context(), "bots.settings.update", up, nil); err != nil {
		WriteCoreError(w, err)
		return
	}
	var after map[string]json.RawMessage
	s.Call(r.Context(), "bots.settings", id, &after)
	audit.Annotate(r, "bot.settings", "", Diff(before, after))
	api.WriteJSON(w, http.StatusOK, map[string]any{"settings": after, "restart_required": true})
}

// Diff returns changed top-level keys as {key: {from, to}}.
func Diff(before, after map[string]json.RawMessage) map[string]any {
	out := map[string]any{}
	for k, v := range after {
		if string(before[k]) != string(v) {
			out[k] = map[string]json.RawMessage{"from": orNull(before[k]), "to": v}
		}
	}
	for k, v := range before {
		if _, ok := after[k]; !ok {
			out[k] = map[string]json.RawMessage{"from": v, "to": json.RawMessage("null")}
		}
	}
	return out
}

func orNull(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

func (s *Service) listSecrets(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var list []secrets.Info
	if err := s.Call(r.Context(), "secrets.list", id, &list); err != nil {
		WriteCoreError(w, err)
		return
	}
	if list == nil {
		list = []secrets.Info{}
	}
	api.WriteJSON(w, http.StatusOK, list)
}

// setSecret takes JSON {"value": "…"} or a multipart "file" (secret file).
func (s *Service) setSecret(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	in := core.SecretSet{BotID: id, Name: r.PathValue("name"), Kind: secrets.KindValue, Actor: api.Actor(r.Context())}
	if f, _, err := r.FormFile("file"); err == nil {
		data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
		f.Close()
		if err != nil || len(data) > 1<<20 {
			api.WriteError(w, http.StatusBadRequest, "bad_request", "secret file must be up to 1 MB")
			return
		}
		in.Kind, in.Value = secrets.KindFile, data
	} else {
		var body struct {
			Value string `json:"value"`
		}
		if !decode(w, r, &body) {
			return
		}
		in.Value = []byte(body.Value)
	}
	if err := s.Call(r.Context(), "secrets.set", in, nil); err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "secret.set", in.Name, map[string]any{"kind": in.Kind, "value": "изменено"})
	if s.OnSecretSet != nil {
		s.OnSecretSet(r.Context(), id, in.Name, api.CallerFrom(r.Context()).Name)
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"name": in.Name, "restart_required": true})
}

func (s *Service) deleteSecret(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := s.Call(r.Context(), "secrets.delete", core.SecretRef{BotID: id, Name: name, Actor: api.Actor(r.Context())}, nil); err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "secret.delete", name, nil)
	w.WriteHeader(http.StatusNoContent)
}

func logQuery(r *http.Request, id int64) core.LogsQuery {
	v := r.URL.Query()
	q := core.LogsQuery{BotID: id, Filter: logs.Filter{MinLevel: v.Get("level"), Query: v.Get("q"), Stream: v.Get("stream")}}
	q.Limit, _ = strconv.Atoi(v.Get("limit"))
	if q.Limit <= 0 {
		q.Limit = 500
	}
	q.From, _ = time.Parse(time.RFC3339, v.Get("from"))
	q.To, _ = time.Parse(time.RFC3339, v.Get("to"))
	return q
}

func (s *Service) logs(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q := logQuery(r, id)
	method := "logs.tail"
	if r.URL.Query().Get("search") == "1" || !q.From.IsZero() || !q.To.IsZero() {
		method = "logs.search"
	}
	var lines []logs.Line
	if err := s.Call(r.Context(), method, q, &lines); err != nil {
		WriteCoreError(w, err)
		return
	}
	if lines == nil {
		lines = []logs.Line{}
	}
	api.WriteJSON(w, http.StatusOK, lines)
}

// logStream is Server-Sent Events: backlog lines, then live ones.
func (s *Service) logStream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c := s.Core.Conn()
	if c == nil {
		WriteCoreError(w, wire.ErrNotConnected)
		return
	}
	q := logQuery(r, id)
	q.Backlog = q.Limit
	st, err := c.OpenStream(r.Context(), "logs.follow", q, wire.StreamOptions{Buffer: 4096, Lossy: true})
	if err != nil {
		WriteCoreError(w, err)
		return
	}
	defer st.Close()
	sse := api.NewSSE(w)
	for {
		var l logs.Line
		if err := st.Recv(&l); err != nil {
			return
		}
		if err := sse.Send(strconv.FormatInt(l.Seq, 10), "line", l); err != nil {
			return
		}
	}
}

func (s *Service) logDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c := s.Core.Conn()
	if c == nil {
		WriteCoreError(w, wire.ErrNotConnected)
		return
	}
	st, err := c.OpenStream(r.Context(), "logs.zip", id, wire.StreamOptions{Buffer: 64})
	if err != nil {
		WriteCoreError(w, err)
		return
	}
	defer st.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="bot-`+strconv.FormatInt(id, 10)+`-logs.zip"`)
	for {
		var chunk []byte
		if err := st.Recv(&chunk); err != nil {
			return
		}
		if _, err := w.Write(chunk); err != nil {
			return
		}
	}
}

func (s *Service) order(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GroupID int64   `json:"group_id"`
		IDs     []int64 `json:"ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Groups.Place(r.Context(), in.GroupID, in.IDs); err != nil {
		api.WriteError(w, http.StatusNotFound, "not_found", "no such group")
		return
	}
	audit.Skip(r) // only the order on the screen changed
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) groups(w http.ResponseWriter, r *http.Request) {
	gs, err := s.Groups.List(r.Context())
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, gs)
}

func groupID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	return id
}

func (s *Service) createGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	g, err := s.Groups.Create(r.Context(), in.Name)
	if err != nil {
		api.WriteError(w, http.StatusConflict, "bad_name", err.Error())
		return
	}
	audit.Annotate(r, "group.create", g.Name, nil)
	api.WriteJSON(w, http.StatusCreated, g)
}

func (s *Service) renameGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.Groups.Rename(r.Context(), groupID(r), in.Name)
	if errors.Is(err, sql.ErrNoRows) {
		api.WriteError(w, http.StatusNotFound, "not_found", "no such group")
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusConflict, "bad_name", err.Error())
		return
	}
	audit.Annotate(r, "group.rename", in.Name, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.Groups.Delete(r.Context(), groupID(r)); err != nil {
		api.WriteError(w, http.StatusNotFound, "not_found", "no such group")
		return
	}
	audit.Annotate(r, "group.delete", strconv.FormatInt(groupID(r), 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) orderGroups(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Groups.Order(r.Context(), in.IDs); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	audit.Skip(r)
	w.WriteHeader(http.StatusNoContent)
}

// putSection replaces one section of the settings document: antiflood,
// texts, tz, stop, crash_updates, updates, releases, mini_app.
func (s *Service) putSection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	sec := r.PathValue("section")
	in := core.SettingPut{BotID: id, Key: sec, Value: string(raw), Actor: api.Actor(r.Context())}
	if err := s.Call(r.Context(), "settings.put", in, nil); err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "settings."+sec, "", map[string]any{"value": raw})
	w.WriteHeader(http.StatusNoContent)
}

// maint pauses a bot (users get the maintenance text, payments wait) or
// ends pause and maintenance.
func (s *Service) maint(method, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in struct {
			Text string `json:"text"`
		}
		if r.ContentLength > 0 && !decode(w, r, &in) {
			return
		}
		if err := s.Call(r.Context(), method, core.MaintReq{BotID: id, Text: in.Text, Actor: api.Actor(r.Context())}, nil); err != nil {
			WriteCoreError(w, err)
			return
		}
		audit.Annotate(r, action, "", nil)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Service) getMaint(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var m *maintenance.M
	if err := s.Call(r.Context(), "maintenance.get", id, &m); err != nil {
		WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"maintenance": m})
}

// schedule plans maintenance; times are in UTC like everything in the panel.
func (s *Service) schedule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p maintenance.Plan
	if !decode(w, r, &p) {
		return
	}
	p.BotID = id
	if err := s.Call(r.Context(), "maintenance.schedule", core.ScheduleReq{Plan: p, Actor: api.Actor(r.Context())}, nil); err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "maintenance.schedule", p.Version, map[string]any{"starts_at": p.StartsAt, "ends_at": p.EndsAt})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) extend(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Minutes int `json:"minutes"`
	}
	if !decode(w, r, &in) {
		return
	}
	req := core.ScheduleReq{Plan: maintenance.Plan{BotID: id}, Minutes: in.Minutes, Actor: api.Actor(r.Context())}
	if err := s.Call(r.Context(), "maintenance.extend", req, nil); err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "maintenance.extend", "", map[string]any{"minutes": in.Minutes})
	w.WriteHeader(http.StatusNoContent)
}

// getProfile reads the Telegram profile for ?lang= (empty = default).
func (s *Service) getProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var v core.ProfileView
	if err := s.Call(r.Context(), "profile.get", core.ProfileReq{BotID: id, Lang: r.URL.Query().Get("lang")}, &v); err != nil {
		WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// setProfile sends only the fields present in the body; errors come per
// field and the other fields are still saved.
func (s *Service) setProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Lang             string                 `json:"lang"`
		Name             *string                `json:"name"`
		Description      *string                `json:"description"`
		ShortDescription *string                `json:"short_description"`
		Commands         *[]telegram.BotCommand `json:"commands"`
		Menu             *telegram.MenuButton   `json:"menu"`
	}
	if !decode(w, r, &in) {
		return
	}
	req := core.ProfileReq{BotID: id, Lang: in.Lang, Name: in.Name, Description: in.Description,
		ShortDescription: in.ShortDescription, Commands: in.Commands, Menu: in.Menu, Actor: api.Actor(r.Context())}
	var out core.ProfileSaved
	if err := s.Call(r.Context(), "profile.set", req, &out); err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "profile.set", in.Lang, nil)
	api.WriteJSON(w, http.StatusOK, out)
}
