package sources

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

func (u *Uploads) registerScheduledDeploys(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/deploy-schedule", u.scheduledDeploy)
	mux.HandleFunc("POST /api/bots/{id}/deploy-schedule", u.scheduleDeploy)
	mux.HandleFunc("DELETE /api/bots/{id}/deploy-schedule/{schedule_id}", u.cancelScheduledDeploy)
}

func (u *Uploads) scheduledDeploy(w http.ResponseWriter, r *http.Request) {
	var plan *core.ScheduledDeploy
	if err := u.Core.Call(r.Context(), "deploys.scheduled", botID(r), &plan); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, struct {
		Schedule *core.ScheduledDeploy `json:"schedule"`
	}{plan})
}

func (u *Uploads) scheduleDeploy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version       string    `json:"version"`
		StartsAt      time.Time `json:"starts_at"`
		ConfigVersion string    `json:"config_version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if body.Version == "" || body.StartsAt.IsZero() {
		api.WriteError(w, http.StatusBadRequest, "bad_request", "укажите версию, дату и время деплоя")
		return
	}
	in := core.ScheduleDeploy{
		SwitchVersion: core.SwitchVersion{BotID: botID(r), Version: body.Version, ConfigVersion: body.ConfigVersion, Actor: api.Actor(r.Context())},
		StartsAt:      body.StartsAt,
	}
	if err := u.Core.Call(r.Context(), "deploys.schedule", in, nil); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "deploy.schedule", in.Version, map[string]any{"starts_at": in.StartsAt})
	w.WriteHeader(http.StatusNoContent)
}

func (u *Uploads) cancelScheduledDeploy(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("schedule_id")
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil || id <= 0 {
		api.WriteError(w, http.StatusBadRequest, "bad_request", "неверный номер плана деплоя")
		return
	}
	in := core.CancelScheduledDeploy{BotRef: core.BotRef{BotID: botID(r), Actor: api.Actor(r.Context())}, ID: id}
	if err := u.Core.Call(r.Context(), "deploys.cancel", in, nil); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "deploy.cancel", key, nil)
	w.WriteHeader(http.StatusNoContent)
}
