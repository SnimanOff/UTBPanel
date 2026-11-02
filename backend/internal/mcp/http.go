package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
)

var releasePath = regexp.MustCompile(`^/api/bots/([0-9]+)/releases$`)

// Direct guards requests that come with the MCP token past /mcp: gate lets
// through only release uploads, and here they get the same check as the
// upload_release tool (write level, the bot's group in full mode). Calls
// of tools reach the API inside the panel and do not pass here.
func (s *Service) Direct(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		docs := r.Method == http.MethodGet && (r.URL.Path == "/llms.txt" || r.URL.Path == "/docs/ai.md")
		if api.CallerFrom(r.Context()).Source != api.SourceMCP || r.URL.Path == "/mcp" || docs {
			next.ServeHTTP(w, r)
			return
		}
		m := releasePath.FindStringSubmatch(r.URL.Path)
		if r.Method != http.MethodPost || m == nil {
			api.WriteError(w, 403, "forbidden", "токен MCP действует только для /mcp и загрузки версии")
			return
		}
		if err := s.allowUpload(r.Context(), m[1]); err != nil {
			code, msg, _ := strings.Cut(err.Error(), ": ")
			api.WriteError(w, 403, code, msg)
			return
		}
		// The deploy stays separate: deploy_release waits for the owner.
		next.ServeHTTP(w, r)
	})
}

func (s *Service) allowUpload(ctx context.Context, bot string) error {
	op, ok := s.ops["upload_release"]
	if !ok {
		return errors.New("forbidden: uploads are not available")
	}
	if s.Groups != nil {
		var unlock func()
		var err error
		ctx, unlock, err = s.Groups.Guard.Lock(ctx)
		if err != nil {
			return errors.New("unavailable: permission check timed out")
		}
		defer unlock()
	}
	return s.authorize(ctx, op, map[string]any{"id": bot})
}

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
	mux.HandleFunc("GET /api/mcp/actions", owner(func(w http.ResponseWriter, r *http.Request) {
		if s.DB == nil {
			api.WriteJSON(w, 200, []Action{})
			return
		}
		list, err := s.Actions(r.Context())
		if err != nil {
			api.WriteError(w, 500, "internal", err.Error())
			return
		}
		api.WriteJSON(w, 200, list)
	}))
	for _, confirm := range []bool{true, false} {
		name := "reject"
		if confirm {
			name = "confirm"
		}
		mux.HandleFunc("POST /api/mcp/actions/{aid}/"+name, owner(func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("aid")
			if s.DB == nil || !actionID(id) {
				api.WriteError(w, 404, "not_found", "запрос не найден")
				return
			}
			a, err := s.Decide(r.Context(), id, confirm, api.CallerFrom(r.Context()).Name)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				api.WriteError(w, 404, "not_found", "запрос не найден")
				return
			case errors.Is(err, errDecided):
				api.WriteError(w, 409, "conflict", err.Error())
				return
			case err != nil:
				api.WriteError(w, 500, "internal", err.Error())
				return
			}
			audit.Annotate(r, "mcp."+name, id, map[string]any{"op": a.Op, "state": a.State})
			api.WriteJSON(w, 200, a)
		}))
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
