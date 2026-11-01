package bots

import (
	"net/http"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/sdkhost"
	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
)

func (s *Service) registerSDKFlags(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/sdk/flags", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var out []sdkhost.Flag
		if err := s.Call(r.Context(), "sdk.flags.list", id, &out); err != nil {
			WriteCoreError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, out)
	})
	for _, reset := range []bool{false, true} {
		method := "PUT"
		if reset {
			method = "DELETE"
		}
		mux.HandleFunc(method+" /api/bots/{id}/sdk/flags/{name}", func(w http.ResponseWriter, r *http.Request) {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			var in struct {
				Value flags.Value `json:"value"`
			}
			if !reset && !decode(w, r, &in) {
				return
			}
			details := map[string]any{"name": r.PathValue("name"), "reset": reset}
			if !reset {
				details["value"] = in.Value
			}
			audit.Annotate(r, "flag.set", "", details)
			if err := s.Call(r.Context(), "sdk.flags.set", core.FlagRequest{BotID: id, Name: r.PathValue("name"), Value: in.Value, Reset: reset, Actor: api.Actor(r.Context())}, nil); err != nil {
				WriteCoreError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
}
