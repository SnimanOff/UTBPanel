// Package settings keeps panel-wide settings in panel.db: the Telegram ID
// of the owner (for "Me" in closed mode, previews and canaries) and others.
package settings

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// Panel are the settings.
type Panel struct {
	OwnerTgID int64 `json:"owner_tg_id"`
}

// Store reads and writes them.
type Store struct{ DB *store.DB }

// Get returns the settings.
func (s *Store) Get(ctx context.Context) Panel {
	var p Panel
	var v string
	if s.DB.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'owner_tg_id'`).Scan(&v) == nil {
		p.OwnerTgID, _ = strconv.ParseInt(v, 10, 64)
	}
	return p
}

// Register adds GET and PUT /api/settings.
func (s *Store) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, http.StatusOK, s.Get(r.Context()))
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		if !api.CallerFrom(r.Context()).IsOwner() {
			api.WriteError(w, http.StatusForbidden, "owner_only", "only the owner key can change panel settings")
			return
		}
		var p Panel
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		_, err := s.DB.W.ExecContext(r.Context(), `INSERT INTO settings (key, value) VALUES ('owner_tg_id', ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, strconv.FormatInt(p.OwnerTgID, 10))
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		audit.Annotate(r, "settings", "owner_tg_id", map[string]any{"owner_tg_id": p.OwnerTgID})
		api.WriteJSON(w, http.StatusOK, p)
	})
}
