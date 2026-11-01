package sources

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

type Exports struct {
	Groups    *bots.Groups
	Core      *wire.Client
	Documents *botconfig.Service
	Metrics   *metrics.Storage
	Extras    *backup.Files
}
type ExportOptions struct {
	BotIDs   []int64 `json:"bot_ids"`
	Password string  `json:"password"`
	backup.Options
}
type Exported struct {
	Password string        `json:"password"`
	Files    []backup.File `json:"files"`
}

func (e *Exports) metrics(ctx context.Context, id int64) (backup.File, error) {
	return e.Extras.Create(ctx, id, "metrics.json", func(w io.Writer) error {
		tx, err := e.Metrics.DB.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := io.WriteString(w, `{"version":1`); err != nil {
			return err
		}
		for _, table := range []string{"m_minute", "m_hour", "m_day", "m_uniq"} {
			if _, err := io.WriteString(w, `,"`+table+`":`); err != nil {
				return err
			}
			if err := backup.Rows(ctx, tx, w, `SELECT * FROM `+table+` WHERE bot_id=?`, id); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "}"); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (e *Exports) make(w http.ResponseWriter, r *http.Request) {
	var in ExportOptions
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		api.WriteError(w, 400, "bad_request", err.Error())
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		api.WriteError(w, 400, "bad_request", "expected one JSON object")
		return
	}
	if in.Password == "" {
		var err error
		in.Password, err = backup.Password()
		if err != nil {
			bots.WriteCoreError(w, err)
			return
		}
	}
	if err := backup.ValidatePassword(in.Password); err != nil {
		api.WriteError(w, 400, "bad_request", err.Error())
		return
	}
	var all []core.Bot
	if err := e.Core.Call(r.Context(), "bots.list", struct{}{}, &all); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	exists := map[int64]bool{}
	for _, b := range all {
		exists[b.ID] = true
	}
	if len(in.BotIDs) == 0 {
		for _, b := range all {
			in.BotIDs = append(in.BotIDs, b.ID)
		}
	}
	seen := map[int64]bool{}
	for _, id := range in.BotIDs {
		if !exists[id] || seen[id] {
			api.WriteError(w, 400, "bad_request", "invalid or duplicate bot id")
			return
		}
		seen[id] = true
	}
	if len(in.BotIDs) == 0 {
		api.WriteError(w, 400, "bad_request", "нет ботов для экспорта")
		return
	}
	out := Exported{Password: in.Password, Files: []backup.File{}}
	success := false
	defer func() {
		if !success {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			for _, f := range out.Files {
				e.Core.Call(cleanup, "exports.delete", backup.FileRef{ID: f.ID, BotID: f.BotID}, nil)
			}
		}
	}()
	for _, id := range in.BotIDs {
		snapshot, err := e.Documents.ExportSnapshot(r.Context(), id)
		if err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		request := backup.ExportRequest{BotID: id, Config: snapshot, Password: in.Password, Options: in.Options, Actor: api.Actor(r.Context())}
		var extra backup.File
		if in.Metrics {
			extra, err = e.metrics(r.Context(), id)
			if err != nil {
				bots.WriteCoreError(w, err)
				return
			}
			request.Metrics = &extra
		}
		var file backup.File
		err = e.Core.Call(r.Context(), "exports.bot", request, &file)
		if in.Metrics {
			e.Extras.Delete(backup.FileRef{ID: extra.ID, BotID: id})
		}
		if err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		out.Files = append(out.Files, file)
	}
	audit.Annotate(r, "bots.export", "", map[string]any{"bot_ids": in.BotIDs, "files": in.Files, "metrics": in.Metrics, "all_versions": in.AllVersions})
	w.Header().Set("Cache-Control", "no-store")
	api.WriteJSON(w, http.StatusCreated, out)
	success = true
}

func (e *Exports) download(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		api.WriteError(w, 400, "bad_request", "invalid bot id")
		return
	}
	ref := backup.FileRef{ID: r.PathValue("export"), BotID: id}
	var file backup.File
	if err := e.Core.Call(r.Context(), "exports.info", ref, &file); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	for ref.Offset < file.Size {
		var chunk []byte
		if err := e.Core.Call(r.Context(), "exports.read", ref, &chunk); err != nil {
			panic(http.ErrAbortHandler)
		}
		if len(chunk) == 0 || int64(len(chunk)) > file.Size-ref.Offset {
			panic(http.ErrAbortHandler)
		}
		if _, err := w.Write(chunk); err != nil {
			return
		}
		ref.Offset += int64(len(chunk))
	}
}
func (e *Exports) Register(mux *api.Mux, coreMux *wire.Mux) {
	e.registerImports(mux)
	mux.HandleFunc("POST /api/exports/bots", e.make)
	mux.HandleFunc("GET /api/bots/{id}/exports/{export}", e.download)
	coreMux.Handle("exports.metrics.read", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) ([]byte, error) { return e.Extras.Read(ref) }))
}
