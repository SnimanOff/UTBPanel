// Package bots is the panel side of bot management: it calls the core for
// everything about processes, tokens and files, and keeps groups and order
// in panel.db.
package bots

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Service serves the bot API of the panel.
type Service struct {
	Core *wire.Client
}

// Call calls the core and maps "not connected" to a clear error.
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

// Register adds the routes.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/bots", s.create)
	mux.HandleFunc("PUT /api/bots/{id}/token", s.setToken)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return false
	}
	return true
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return
	}
	var b core.Bot
	if err := s.Call(r.Context(), "bots.create", core.CreateBot{Name: in.Name, Token: in.Token}, &b); err != nil {
		WriteCoreError(w, err)
		return
	}
	// The token is never returned: only @username is shown from now on.
	api.WriteJSON(w, http.StatusCreated, b)
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
	if err := s.Call(r.Context(), "bots.set_token", core.SetToken{BotID: id, Token: in.Token}, &b); err != nil {
		WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, b)
}
