package users

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// API is the panel side of the "Users" tab.
type API struct {
	Core *wire.Client
	// WriteErr translates core errors (bots.WriteCoreError).
	WriteErr func(http.ResponseWriter, error)
}

// Register adds the routes.
func (a *API) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/users", a.list)
	mux.HandleFunc("GET /api/bots/{id}/audience", a.audience)
	mux.HandleFunc("POST /api/bots/{id}/users/{uid}/ban", a.ban(false))
	mux.HandleFunc("POST /api/users/{uid}/ban", a.ban(true))
	mux.HandleFunc("POST /api/bots/{id}/users/{uid}/unban", a.unban)
}

func botID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	q := ListQuery{BotID: botID(r), Query: v.Get("q"), Filter: v.Get("filter")}
	q.Limit, _ = strconv.Atoi(v.Get("limit"))
	q.Offset, _ = strconv.Atoi(v.Get("offset"))
	var res ListResult
	if err := a.Core.Call(r.Context(), "users.list", q, &res); err != nil {
		a.WriteErr(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, res)
}

func (a *API) audience(w http.ResponseWriter, r *http.Request) {
	var res Audience
	if err := a.Core.Call(r.Context(), "users.audience", botID(r), &res); err != nil {
		a.WriteErr(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, res)
}

// banReq mirrors core.BanReq.
type banReq struct {
	BotID  int64         `cbor:"1,keyasint"`
	UserID int64         `cbor:"2,keyasint"`
	Reason string        `cbor:"3,keyasint,omitempty"`
	Hours  int           `cbor:"4,keyasint,omitempty"`
	All    bool          `cbor:"5,keyasint,omitempty"`
	Actor  journal.Actor `cbor:"9,keyasint"`
}

func (a *API) ban(all bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reason string `json:"reason"`
			Hours  int    `json:"hours"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		uid, _ := strconv.ParseInt(r.PathValue("uid"), 10, 64)
		req := banReq{BotID: botID(r), UserID: uid, Reason: in.Reason, Hours: in.Hours, All: all, Actor: api.Actor(r.Context())}
		if err := a.Core.Call(r.Context(), "users.ban", req, nil); err != nil {
			a.WriteErr(w, err)
			return
		}
		action := "user.ban"
		if all {
			action = "user.ban_all"
		}
		audit.Annotate(r, action, strconv.FormatInt(uid, 10), map[string]any{"reason": in.Reason, "hours": in.Hours})
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) unban(w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err := a.Core.Call(r.Context(), "users.unban", banReq{BotID: botID(r), UserID: uid, Actor: api.Actor(r.Context())}, nil); err != nil {
		a.WriteErr(w, err)
		return
	}
	audit.Annotate(r, "user.unban", strconv.FormatInt(uid, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}
