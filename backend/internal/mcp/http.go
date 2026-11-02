package mcp

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
)

func (s *Service) Register(mux *api.Mux) {
	owner := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			caller := api.CallerFrom(r.Context())
			if !caller.IsOwner() || caller.Source == api.SourceMCP {
				api.WriteError(w, 403, "owner_only", "режимы MCP меняет владелец в панели")
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /api/mcp/groups", owner(func(w http.ResponseWriter, r *http.Request) {
		groups, err := s.Groups.List(r.Context())
		if err != nil {
			api.WriteError(w, 500, "internal", err.Error())
			return
		}
		api.WriteJSON(w, 200, groups)
	}))
	mux.HandleFunc("PUT /api/mcp/groups/{gid}", owner(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
		if err != nil || id <= 0 {
			api.WriteError(w, 400, "bad_request", "выберите группу")
			return
		}
		var in struct {
			Mode string `json:"mode"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&in); err != nil {
			api.WriteError(w, 400, "bad_request", "нужен объект с mode: off, read или full")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			api.WriteError(w, 400, "bad_request", "ожидается один объект")
			return
		}
		if in.Mode != "off" && in.Mode != "read" && in.Mode != "full" {
			api.WriteError(w, 400, "bad_request", "режим off, read или full")
			return
		}
		previous, err := s.Groups.SetMCPMode(r.Context(), id, in.Mode)
		if errors.Is(err, sql.ErrNoRows) {
			api.WriteError(w, 404, "not_found", "группа не найдена")
			return
		}
		if err != nil {
			api.WriteError(w, 500, "internal", err.Error())
			return
		}
		audit.Annotate(r, "mcp.mode", strconv.FormatInt(id, 10), map[string]any{"from": previous, "to": in.Mode})
		w.WriteHeader(204)
	}))
}
