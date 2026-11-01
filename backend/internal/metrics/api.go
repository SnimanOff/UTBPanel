package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/users"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// API serves the metrics history.
type API struct {
	S    *Storage
	Core *wire.Client
}

// Register adds the routes.
func (a *API) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/metrics", a.bot)
}

func (a *API) bot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		api.WriteError(w, http.StatusBadRequest, "bad_request", "bad bot id")
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "24h"
	}
	s, err := a.S.Query(r.Context(), id, period, time.Now())
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	resp := map[string]any{"series": s}
	var from int64
	if p, ok := Periods[period]; ok && p.Span > 0 {
		from = time.Now().Add(-p.Span).Unix()
	}
	var marks []FlagMark
	if a.Core.Call(r.Context(), "sdk.flags.marks", FlagMarksRequest{BotID: id, From: from}, &marks) == nil {
		resp["flags"] = marks
	}
	var us users.Stats
	if a.Core.Call(r.Context(), "users.stats", id, &us) == nil {
		resp["users"] = us
	}
	api.WriteJSON(w, http.StatusOK, resp)
}
