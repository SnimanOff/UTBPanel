package users

import (
	"net/http"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
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
