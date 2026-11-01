package dbgate

import (
	"net/http"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// backupRef mirrors core.BackupRef (the panel does not import the core).
type backupRef struct {
	BotID int64         `cbor:"1,keyasint"`
	Name  string        `cbor:"2,keyasint,omitempty"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

// RegisterBackups adds the backup routes.
func (a *API) RegisterBackups(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/db/backups", func(w http.ResponseWriter, r *http.Request) {
		var bs []Backup
		if err := a.Core.Call(r.Context(), "db.backups", id(r), &bs); err != nil {
			a.WriteErr(w, err)
			return
		}
		if bs == nil {
			bs = []Backup{}
		}
		api.WriteJSON(w, http.StatusOK, bs)
	})
	mux.HandleFunc("POST /api/bots/{id}/db/backups", func(w http.ResponseWriter, r *http.Request) {
		var b Backup
		if err := a.Core.Call(r.Context(), "db.backup.make", backupRef{BotID: id(r), Actor: api.Actor(r.Context())}, &b); err != nil {
			a.WriteErr(w, err)
			return
		}
		audit.Annotate(r, "db.backup", b.Name, nil)
		api.WriteJSON(w, http.StatusCreated, b)
	})
	mux.HandleFunc("POST /api/bots/{id}/db/backups/{name}/restore", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := a.Core.Call(r.Context(), "db.backup.restore", backupRef{BotID: id(r), Name: name, Actor: api.Actor(r.Context())}, nil); err != nil {
			a.WriteErr(w, err)
			return
		}
		audit.Annotate(r, "db.restore", name, nil)
		api.WriteJSON(w, http.StatusOK, map[string]string{"restored": name})
	})
	mux.HandleFunc("GET /api/bots/{id}/db/backups/{name}", func(w http.ResponseWriter, r *http.Request) {
		c := a.Core.Conn()
		if c == nil {
			a.WriteErr(w, wire.ErrNotConnected)
			return
		}
		name := r.PathValue("name")
		st, err := c.OpenStream(r.Context(), "db.backup.download", backupRef{BotID: id(r), Name: name}, wire.StreamOptions{Buffer: 256})
		if err != nil {
			a.WriteErr(w, err)
			return
		}
		defer st.Close()
		w.Header().Set("Content-Type", "application/vnd.sqlite3")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		for {
			var chunk []byte
			if err := st.Recv(&chunk); err != nil {
				return
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
}
