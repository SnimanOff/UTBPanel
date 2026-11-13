package panel

import (
	"encoding/json"
	"net/http"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

// registerPages adds the routes of static pages (core/pages.go).
func (p *Panel) registerPages(mux *api.Mux) {
	mux.HandleFunc("GET /api/pages", func(w http.ResponseWriter, r *http.Request) {
		out := []core.PageInfo{}
		if err := p.Core.Call(r.Context(), "pages.list", struct{}{}, &out); err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /api/pages/{sub}", func(w http.ResponseWriter, r *http.Request) {
		var pg core.Page
		if err := p.Core.Call(r.Context(), "pages.get", r.PathValue("sub"), &pg); err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, pg)
	})
	// PUT creates or replaces the page {sub}; "sub" in the body renames it.
	mux.HandleFunc("PUT /api/pages/{sub}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Sub  string `json:"sub"`
			HTML string `json:"html"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, core.MaxPageSize*2)).Decode(&in); err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		req := core.PageReq{Sub: r.PathValue("sub"), HTML: in.HTML, Actor: api.Actor(r.Context())}
		if in.Sub != "" && in.Sub != req.Sub {
			req.Old, req.Sub = req.Sub, in.Sub
		}
		var info core.PageInfo
		if err := p.Core.Call(r.Context(), "pages.put", req, &info); err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		audit.Annotate(r, "pages.put", info.Sub, map[string]any{"size": info.Size, "old": req.Old})
		api.WriteJSON(w, http.StatusOK, info)
	})
	mux.HandleFunc("DELETE /api/pages/{sub}", func(w http.ResponseWriter, r *http.Request) {
		sub := r.PathValue("sub")
		if err := p.Core.Call(r.Context(), "pages.delete", core.PageReq{Sub: sub, Actor: api.Actor(r.Context())}, nil); err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		audit.Annotate(r, "pages.delete", sub, nil)
		w.WriteHeader(http.StatusNoContent)
	})
}
