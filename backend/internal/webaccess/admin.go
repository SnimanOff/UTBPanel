package webaccess

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
)

// Register adds the owner's "Access" API to the main mux.
func (s *Service) Register(mux *api.Mux) {
	owner := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c := api.CallerFrom(r.Context())
			if !c.IsOwner() || c.Source == api.SourceMCP {
				api.WriteError(w, http.StatusForbidden, "owner_only", "доступы по passkey выдаёт только владелец")
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /api/access", owner(s.list))
	mux.HandleFunc("POST /api/access", owner(s.create))
	mux.HandleFunc("POST /api/access/{id}/invite", owner(s.reinvite))
	mux.HandleFunc("PUT /api/access/{id}/expiry", owner(s.setExpiry))
	mux.HandleFunc("DELETE /api/access/{id}", owner(s.remove))
	mux.HandleFunc("POST /api/access/credentials/{cid}/approve", owner(s.approve))
	mux.HandleFunc("DELETE /api/access/credentials/{cid}", owner(s.removeCred))
}

func (s *Service) inviteURL(token string) string { return s.RP.Origin + "/invite/" + token }

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.List(r.Context())
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"enabled": s.Enabled(), "url": s.RP.Origin, "accesses": list})
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled() {
		api.WriteError(w, http.StatusConflict, "no_domain", "вход по passkey работает только с доменом: задайте DOMAIN в .env")
		return
	}
	var in struct {
		Name    string  `json:"name"`
		Role    string  `json:"role"`
		AllBots bool    `json:"all_bots"`
		Bots    []int64 `json:"bots"`
		Days    int     `json:"days"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case !ValidDays(in.Days):
		api.WriteError(w, http.StatusBadRequest, "bad_request", "срок: 1, 7, 14 или 30 дней, 0 — бессрочно")
		return
	case in.Name == "" || len(in.Name) > 60:
		api.WriteError(w, http.StatusBadRequest, "bad_request", "имя: от 1 до 60 символов")
		return
	case in.Role != RoleView && in.Role != RoleManage && in.Role != RoleFull:
		api.WriteError(w, http.StatusBadRequest, "bad_request", "роль: view, manage или full")
		return
	case in.Role == RoleFull && !in.AllBots:
		api.WriteError(w, http.StatusBadRequest, "bad_request", "все права — только вместе со всеми ботами")
		return
	case !in.AllBots && len(in.Bots) == 0:
		api.WriteError(w, http.StatusBadRequest, "bad_request", "выберите ботов")
		return
	}
	if in.AllBots {
		in.Bots = nil
	}
	a, token, err := s.Store.Create(r.Context(), Access{Name: in.Name, Role: in.Role, AllBots: in.AllBots, Bots: in.Bots, CreatedBy: api.CallerFrom(r.Context()).Name, ExpiresAt: Until(in.Days)})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	audit.Annotate(r, "access.create", strconv.FormatInt(a.ID, 10), map[string]any{"name": a.Name, "role": a.Role, "all_bots": a.AllBots, "bots": a.Bots, "days": in.Days})
	api.WriteJSON(w, http.StatusCreated, map[string]any{"access": a, "url": s.inviteURL(token), "expires_at": time.Now().Add(InviteTTL).UTC()})
}

func pathInt(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (s *Service) storeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		api.WriteError(w, http.StatusNotFound, "not_found", "нет такого доступа")
		return
	}
	if errors.Is(err, ErrAccessEnded) {
		api.WriteError(w, http.StatusConflict, "access_expired", "срок доступа истёк: сначала продлите его")
		return
	}
	api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
}

// setExpiry gives an access a new term counted from now: 1, 7, 14 or 30
// days, 0 for good.
func (s *Service) setExpiry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Days int `json:"days"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !ValidDays(in.Days) {
		api.WriteError(w, http.StatusBadRequest, "bad_request", "срок: 1, 7, 14 или 30 дней, 0 — бессрочно")
		return
	}
	until := Until(in.Days)
	if err := s.Store.SetExpiry(r.Context(), pathInt(r), until); err != nil {
		s.storeError(w, err)
		return
	}
	audit.Annotate(r, "access.expiry", r.PathValue("id"), map[string]any{"days": in.Days})
	out := map[string]any{}
	if until != nil {
		out["expires_at"] = until
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) reinvite(w http.ResponseWriter, r *http.Request) {
	token, err := s.Store.Invite(r.Context(), pathInt(r))
	if err != nil {
		s.storeError(w, err)
		return
	}
	audit.Annotate(r, "access.invite", r.PathValue("id"), nil)
	api.WriteJSON(w, http.StatusOK, map[string]any{"url": s.inviteURL(token), "expires_at": time.Now().Add(InviteTTL).UTC()})
}

func (s *Service) remove(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Delete(r.Context(), pathInt(r)); err != nil {
		s.storeError(w, err)
		return
	}
	audit.Annotate(r, "access.delete", r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) approve(w http.ResponseWriter, r *http.Request) {
	id, err := b64.DecodeString(r.PathValue("cid"))
	if err == nil {
		err = s.Store.Approve(r.Context(), id)
	}
	if err != nil {
		s.storeError(w, ErrNotFound)
		return
	}
	audit.Annotate(r, "access.approve", r.PathValue("cid"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) removeCred(w http.ResponseWriter, r *http.Request) {
	id, err := b64.DecodeString(r.PathValue("cid"))
	if err == nil {
		err = s.Store.RemoveCredential(r.Context(), id)
	}
	if err != nil {
		s.storeError(w, ErrNotFound)
		return
	}
	audit.Annotate(r, "access.remove_passkey", r.PathValue("cid"), nil)
	w.WriteHeader(http.StatusNoContent)
}
