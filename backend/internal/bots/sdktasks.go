package bots

import (
	"net/http"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/sdkhost"
)

func (s *Service) registerSDKTasks(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/sdk/tasks", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var view sdkhost.TasksView
		if err := s.Call(r.Context(), "sdk.tasks.list", id, &view); err != nil {
			WriteCoreError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, view)
	})
	mux.HandleFunc("POST /api/bots/{id}/sdk/tasks/{name}/run", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		name := r.PathValue("name")
		audit.Annotate(r, "sdk.tasks.run", "", map[string]any{"name": name})
		var runID int64
		if err := s.Call(r.Context(), "sdk.tasks.run", core.TaskRequest{BotID: id, Name: name, Actor: api.Actor(r.Context())}, &runID); err != nil {
			WriteCoreError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusAccepted, map[string]int64{"id": runID})
	})
}
