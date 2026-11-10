package bots

import (
	"net/http"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

// Test runs of a new version on a test bot before it is deployed (see
// core/trial.go).
func (s *Service) registerTrial(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/trial", s.getTrial)
	mux.HandleFunc("POST /api/bots/{id}/trial", s.startTrial)
	mux.HandleFunc("POST /api/bots/{id}/trial/promote", s.trialOp("trial.promote"))
	mux.HandleFunc("DELETE /api/bots/{id}/trial", s.trialOp("trial.stop"))
}

func (s *Service) getTrial(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var out core.TrialInfo
	if err := s.Call(r.Context(), "trial.get", id, &out); err != nil {
		WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) startTrial(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Version    string `json:"version"`
		TestBotID  int64  `json:"test_bot_id"`
		CopyConfig *bool  `json:"copy_config"`
	}
	if !decode(w, r, &in) {
		return
	}
	cp := in.CopyConfig == nil || *in.CopyConfig
	req := core.TrialReq{BotID: id, Version: in.Version, TestBotID: in.TestBotID, CopyConfig: cp, Actor: api.Actor(r.Context())}
	var t *core.Trial
	if err := s.Call(r.Context(), "trial.start", req, &t); err != nil {
		WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "trial.start", in.Version, map[string]any{"test_bot": in.TestBotID, "copy_config": cp})
	api.WriteJSON(w, http.StatusOK, t)
}

// trialOp promotes or stops a trial. "keep_test_bot": true leaves the test
// bot running.
func (s *Service) trialOp(method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in struct {
			ConfigVersion string `json:"config_version"`
			KeepTestBot   bool   `json:"keep_test_bot"`
		}
		if r.ContentLength > 0 && !decode(w, r, &in) {
			return
		}
		req := core.TrialReq{BotID: id, ConfigVersion: in.ConfigVersion, KeepTestBot: in.KeepTestBot, Actor: api.Actor(r.Context())}
		if err := s.Call(r.Context(), method, req, nil); err != nil {
			WriteCoreError(w, err)
			return
		}
		audit.Annotate(r, method, "", nil)
		w.WriteHeader(http.StatusNoContent)
	}
}
