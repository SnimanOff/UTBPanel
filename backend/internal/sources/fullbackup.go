package sources

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

type FullBackups struct {
	Mu      *sync.Mutex
	Blocked func() bool
	Core    *wire.Client
	DB      *store.DB
	W       *store.Writer
	PKI     string
	Extras  *backup.Files
}

func (f *FullBackups) prepare(ctx context.Context) (backup.PanelPart, error) {
	if f.Mu != nil {
		f.Mu.Lock()
		defer f.Mu.Unlock()
	}
	if f.Blocked != nil && f.Blocked() {
		return backup.PanelPart{}, wire.Errorf(wire.CodeUnavailable, "выполняется восстановление")
	}
	if err := f.W.Now(ctx, func(*sql.Tx) error { return nil }); err != nil {
		return backup.PanelPart{}, err
	}
	tmp, err := os.MkdirTemp("", "utbpanel-part-")
	if err != nil {
		return backup.PanelPart{}, err
	}
	defer os.RemoveAll(tmp)
	if err := dbgate.Snapshot(ctx, f.DB.Path, filepath.Join(tmp, "panel.db"), -1); err != nil {
		return backup.PanelPart{}, err
	}
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return backup.PanelPart{}, err
	}
	defer root.Close()
	pki, err := os.OpenRoot(f.PKI)
	if err != nil {
		return backup.PanelPart{}, err
	}
	defer pki.Close()
	for _, name := range []string{"ca.crt", "ca.key"} {
		info, err := pki.Lstat(name)
		if err != nil {
			return backup.PanelPart{}, err
		}
		if !info.Mode().IsRegular() {
			return backup.PanelPart{}, os.ErrInvalid
		}
	}
	file, err := f.Extras.Create(ctx, 0, "panel-part.tar", func(out io.Writer) error {
		return backup.Parts(ctx, out, func(a *backup.Archive) error {
			if err := a.RootFile(root, "panel.db", "panel.db", false); err != nil {
				return err
			}
			return a.Tree(pki, ".", "pki")
		})
	})
	return backup.PanelPart{File: file, Version: buildinfo.String("panel")}, err
}

func (f *FullBackups) Register(mux *api.Mux, coreMux *wire.Mux) {
	coreMux.Handle("backups.panel.prepare", wire.Func(func(ctx context.Context, _ *wire.Conn, _ struct{}) (backup.PanelPart, error) { return f.prepare(ctx) }))
	coreMux.Handle("backups.panel.read", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) ([]byte, error) { return f.Extras.Read(ref) }))
	coreMux.Handle("backups.panel.release", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) (struct{}, error) {
		return struct{}{}, f.Extras.Delete(ref)
	}))
	owner := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !api.CallerFrom(r.Context()).IsOwner() {
				api.WriteError(w, http.StatusForbidden, "owner_only", "полный бэкап доступен только владельцу")
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /api/backups", owner(func(w http.ResponseWriter, r *http.Request) {
		var out backup.FullStatus
		if err := f.Core.Call(r.Context(), "backups.status", struct{}{}, &out); err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		api.WriteJSON(w, 200, out)
	}))
	mux.HandleFunc("PUT /api/backups/settings", owner(func(w http.ResponseWriter, r *http.Request) {
		var cfg backup.FullSettings
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			api.WriteError(w, 400, "bad_request", err.Error())
			return
		}
		if err := f.Core.Call(r.Context(), "backups.settings", cfg, nil); err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		audit.Annotate(r, "backup.settings", "", map[string]any{"enabled": cfg.Enabled, "cron": cfg.Cron, "keep": cfg.Keep, "files": cfg.Files})
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/backups", owner(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Files *bool `json:"files"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil && err != io.EOF {
			api.WriteError(w, 400, "bad_request", err.Error())
			return
		}
		var out backup.File
		if err := f.Core.Call(r.Context(), "backups.make", core.FullBackupRequest{Files: in.Files, Actor: api.Actor(r.Context())}, &out); err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		audit.Annotate(r, "backup.make", out.Name, nil)
		api.WriteJSON(w, http.StatusCreated, out)
	}))
	mux.HandleFunc("GET /api/backups/{backup}", owner(f.download))
}
func (f *FullBackups) download(w http.ResponseWriter, r *http.Request) {
	ref := backup.FileRef{ID: r.PathValue("backup")}
	var info backup.File
	if err := f.Core.Call(r.Context(), "backups.info", ref, &info); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name}))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	if r.Method == http.MethodHead {
		return
	}
	for ref.Offset < info.Size {
		var raw []byte
		if err := f.Core.Call(r.Context(), "backups.read", ref, &raw); err != nil {
			panic(http.ErrAbortHandler)
		}
		if len(raw) == 0 || int64(len(raw)) > info.Size-ref.Offset {
			panic(http.ErrAbortHandler)
		}
		if _, err := w.Write(raw); err != nil {
			return
		}
		ref.Offset += int64(len(raw))
	}
	ack, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	f.Core.Call(ack, "backups.downloaded", backup.FileRef{ID: ref.ID}, nil)
}
