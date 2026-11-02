package secretreq

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
)

// API serves the requests: an AI asks, the owner answers.
type API struct {
	Store *Store
	// Exists checks the bot of a request.
	Exists func(ctx context.Context, botID int64) error
	// CreateBot makes the bot of a new-bot request from the owner's token
	// (the core checks it with getMe); it returns the new id.
	CreateBot func(ctx context.Context, name, token string, groupID int64) (int64, string, error)
	// WriteError reports core errors (Telegram's answer as is).
	WriteError func(http.ResponseWriter, error)
}

func (a *API) Register(mux *api.Mux) {
	owner := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			caller := api.CallerFrom(r.Context())
			if !caller.IsOwner() || caller.Source == api.SourceMCP {
				api.WriteError(w, 403, "owner_only", "запросы секретов закрывает владелец в панели")
				return
			}
			if !ValidID(r.PathValue("rid")) && r.PathValue("rid") != "" {
				api.WriteError(w, 404, "not_found", "запрос не найден")
				return
			}
			next(w, r)
		}
	}
	bot := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			api.WriteError(w, 400, "bad_request", "invalid bot id")
			return 0, false
		}
		if a.Exists != nil {
			if err := a.Exists(r.Context(), id); err != nil {
				a.WriteError(w, err)
				return 0, false
			}
		}
		return id, true
	}
	mux.HandleFunc("POST /api/bots/{id}/secret-requests", func(w http.ResponseWriter, r *http.Request) {
		id, ok := bot(w, r)
		if !ok {
			return
		}
		var in struct {
			Name string `json:"name"`
			Hint string `json:"hint"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
			api.WriteError(w, 400, "bad_request", "нужен объект с name и hint")
			return
		}
		req, err := a.Store.Ask(r.Context(), api.CallerFrom(r.Context()), id, in.Name, in.Hint, "", 0)
		if err != nil {
			api.WriteError(w, 400, "bad_request", err.Error())
			return
		}
		audit.Annotate(r, "secret.request", in.Name, map[string]any{"id": req.ID})
		api.WriteJSON(w, 201, req)
	})
	mux.HandleFunc("GET /api/bots/{id}/secret-requests", func(w http.ResponseWriter, r *http.Request) {
		id, ok := bot(w, r)
		if !ok {
			return
		}
		list, err := a.Store.List(r.Context(), id, false)
		if err != nil {
			api.WriteError(w, 500, "internal", err.Error())
			return
		}
		api.WriteJSON(w, 200, list)
	})
	mux.HandleFunc("GET /api/secret-requests", owner(func(w http.ResponseWriter, r *http.Request) {
		list, err := a.Store.List(r.Context(), -1, true)
		if err != nil {
			api.WriteError(w, 500, "internal", err.Error())
			return
		}
		api.WriteJSON(w, 200, list)
	}))
	mux.HandleFunc("POST /api/secret-requests/{rid}/reject", owner(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("rid")
		if !a.write(w, a.Store.Reject(r.Context(), id, api.CallerFrom(r.Context()).Name)) {
			return
		}
		audit.Annotate(r, "secret.request_reject", id, nil)
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /api/secret-requests/{rid}/token", owner(func(w http.ResponseWriter, r *http.Request) {
		req, err := a.Store.Get(r.Context(), r.PathValue("rid"))
		if !a.write(w, err) {
			return
		}
		if req.BotID != 0 || req.Final() {
			a.write(w, ErrDecided)
			return
		}
		var in struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || in.Token == "" {
			api.WriteError(w, 400, "bad_request", "нужен token")
			return
		}
		// getMe failures come back to the owner; the request keeps waiting.
		botID, username, err := a.CreateBot(r.Context(), req.BotName, in.Token, req.GroupID)
		if err != nil {
			a.WriteError(w, err)
			return
		}
		a.Store.Created(r.Context(), req.ID, botID, api.CallerFrom(r.Context()).Name)
		audit.Annotate(r, "bot.create", req.BotName, map[string]any{"username": username, "token": "изменено", "request": req.ID})
		audit.SetBot(r, botID)
		api.WriteJSON(w, 201, map[string]any{"bot_id": botID, "username": username})
	}))
}

func (a *API) write(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, sql.ErrNoRows):
		api.WriteError(w, 404, "not_found", "запрос не найден")
	case errors.Is(err, ErrDecided):
		api.WriteError(w, 409, "conflict", err.Error())
	default:
		api.WriteError(w, 500, "internal", err.Error())
	}
	return false
}
