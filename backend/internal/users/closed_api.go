package users

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
)

type closedReq struct {
	BotID   int64         `cbor:"1,keyasint"`
	Enabled bool          `cbor:"2,keyasint,omitempty"`
	Text    string        `cbor:"3,keyasint,omitempty"`
	User    string        `cbor:"4,keyasint,omitempty"`
	Note    string        `cbor:"5,keyasint,omitempty"`
	UserID  int64         `cbor:"6,keyasint,omitempty"`
	Actor   journal.Actor `cbor:"9,keyasint"`
}

// RegisterClosed adds the closed mode routes; owner returns the Telegram ID
// of the owner for the "Me" button.
func (a *API) RegisterClosed(mux *api.Mux, owner func(r *http.Request) int64) {
	mux.HandleFunc("GET /api/bots/{id}/closed", func(w http.ResponseWriter, r *http.Request) {
		var st ClosedState
		if err := a.Core.Call(r.Context(), "closed.get", botID(r), &st); err != nil {
			a.WriteErr(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("PUT /api/bots/{id}/closed", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled bool   `json:"enabled"`
			Text    string `json:"text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		req := closedReq{BotID: botID(r), Enabled: in.Enabled, Text: in.Text, Actor: api.Actor(r.Context())}
		if err := a.Core.Call(r.Context(), "closed.set", req, nil); err != nil {
			a.WriteErr(w, err)
			return
		}
		audit.Annotate(r, "closed.set", "", map[string]any{"enabled": in.Enabled, "text": in.Text})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/bots/{id}/closed/allow", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			User string `json:"user"` // id, @username or "me"
			Note string `json:"note"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if in.User == "me" {
			id := owner(r)
			if id == 0 {
				api.WriteError(w, http.StatusBadRequest, "no_owner_id", "укажите свой Telegram ID в настройках панели")
				return
			}
			in.User = strconv.FormatInt(id, 10)
			if in.Note == "" {
				in.Note = "владелец"
			}
		}
		var id int64
		if err := a.Core.Call(r.Context(), "closed.allow", closedReq{BotID: botID(r), User: in.User, Note: in.Note, Actor: api.Actor(r.Context())}, &id); err != nil {
			a.WriteErr(w, err)
			return
		}
		audit.Annotate(r, "closed.allow", strconv.FormatInt(id, 10), nil)
		api.WriteJSON(w, http.StatusOK, map[string]int64{"user_id": id})
	})
	mux.HandleFunc("DELETE /api/bots/{id}/closed/allow/{uid}", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseInt(r.PathValue("uid"), 10, 64)
		if err := a.Core.Call(r.Context(), "closed.remove", closedReq{BotID: botID(r), UserID: uid, Actor: api.Actor(r.Context())}, nil); err != nil {
			a.WriteErr(w, err)
			return
		}
		audit.Annotate(r, "closed.remove", strconv.FormatInt(uid, 10), nil)
		w.WriteHeader(http.StatusNoContent)
	})
}
