package bots

import (
	"net/http"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/sdkhost"
)

func (s *Service) registerSDKMetrics(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/sdk/metrics", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var metrics []sdkhost.Metric
		if err := s.Call(r.Context(), "sdk.metrics.list", id, &metrics); err != nil {
			WriteCoreError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, metrics)
	})
	mux.HandleFunc("PUT /api/bots/{id}/sdk/metrics/selected", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in struct {
			Names []string `json:"names"`
		}
		if !decode(w, r, &in) {
			return
		}
		audit.Annotate(r, "sdk.metrics.select", "", map[string]any{"names": in.Names})
		if err := s.Call(r.Context(), "sdk.metrics.select", core.MetricSelection{BotID: id, Names: in.Names, Actor: api.Actor(r.Context())}, nil); err != nil {
			WriteCoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
