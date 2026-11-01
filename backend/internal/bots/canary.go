package bots

import (
	"net/http"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

func (s *Service) registerCanary(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/canary", s.getCanary)
	mux.HandleFunc("POST /api/bots/{id}/canary", s.canaryOp("canary.start"))
	mux.HandleFunc("PATCH /api/bots/{id}/canary", s.canaryOp("canary.set"))
	mux.HandleFunc("DELETE /api/bots/{id}/canary", s.canaryOp("canary.stop"))
	mux.HandleFunc("POST /api/bots/{id}/canary/promote", s.canaryOp("canary.promote"))
	mux.HandleFunc("GET /api/bots/{id}/crash-updates", s.crashUpdates)
}

func (s *Service) getCanary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var cn *core.Canary
	if err := s.Call(r.Context(), "canary.get", id, &cn); err != nil {
		WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"canary": cn})
}

// canaryOp starts, changes or stops the canary. "me": true adds the
// owner's Telegram ID to the users.
func (s *Service) canaryOp(method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in struct {
			Version   string  `json:"version"`
			Share     int     `json:"share"`
			Users     []int64 `json:"users"`
			Me        bool    `json:"me"`
			AutoAfter int     `json:"auto_after"`
		}
		if r.ContentLength > 0 && !decode(w, r, &in) {
			return
		}
		if in.Me {
			me := int64(0)
			if s.Owner != nil {
				me = s.Owner(r)
			}
			if me == 0 {
				api.WriteError(w, http.StatusBadRequest, "no_owner_id", "укажите свой Telegram ID в настройках панели")
				return
			}
			in.Users = append(in.Users, me)
		}
		req := core.CanaryReq{BotID: id, Version: in.Version, Share: in.Share, Users: in.Users, AutoAfter: in.AutoAfter, Actor: api.Actor(r.Context())}
		if err := s.Call(r.Context(), method, req, nil); err != nil {
			WriteCoreError(w, err)
			return
		}
		audit.Annotate(r, method, in.Version, map[string]any{"share": in.Share, "users": len(in.Users)})
		w.WriteHeader(http.StatusNoContent)
	}
}

// crashUpdates lists updates kept at crashes (crash_updates setting).
func (s *Service) crashUpdates(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var out []core.CrashUpdate
	if err := s.Call(r.Context(), "crash_updates.list", id, &out); err != nil {
		WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}
