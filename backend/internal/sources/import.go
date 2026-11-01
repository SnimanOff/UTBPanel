package sources

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
)

type importEdit struct {
	Name    string `json:"name"`
	Token   string `json:"token"`
	Start   bool   `json:"start"`
	Secrets map[string]struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	} `json:"secrets"`
}
type Imported struct {
	Bot      core.Bot `json:"bot"`
	Started  bool     `json:"started"`
	Warnings []string `json:"warnings"`
}

func (e *Exports) abort(ctx context.Context, id string) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	e.Core.Call(cleanup, "imports.abort", id, nil)
}
func (e *Exports) uploadImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, backup.MaxImportBytes+(1<<20))
	multipart, err := r.MultipartReader()
	if err != nil {
		api.WriteError(w, 400, "bad_request", "нужен multipart/form-data с file и password")
		return
	}
	var id string
	if err := e.Core.Call(r.Context(), "imports.begin", struct{}{}, &id); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	success := false
	defer func() {
		if !success {
			e.abort(r.Context(), id)
		}
	}()
	password := ""
	fileSeen := false
	buffer := make([]byte, backup.ChunkSize)
	offset := int64(0)
	for {
		part, err := multipart.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			api.WriteError(w, 400, "bad_request", err.Error())
			return
		}
		switch part.FormName() {
		case "password":
			raw, err := io.ReadAll(io.LimitReader(part, 4097))
			if err != nil || len(raw) > 4096 {
				api.WriteError(w, 400, "bad_request", "слишком длинный пароль")
				return
			}
			password = string(raw)
		case "file":
			if fileSeen {
				api.WriteError(w, 400, "bad_request", "ожидается один архив")
				return
			}
			fileSeen = true
			for {
				n, err := io.ReadFull(part, buffer)
				if n > 0 {
					if callErr := e.Core.Call(r.Context(), "imports.chunk", backup.ImportChunk{ID: id, Offset: offset, Data: buffer[:n]}, nil); callErr != nil {
						bots.WriteCoreError(w, callErr)
						return
					}
					offset += int64(n)
				}
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					break
				}
				if err != nil {
					api.WriteError(w, 400, "bad_request", err.Error())
					return
				}
			}
		default:
			api.WriteError(w, 400, "bad_request", "неизвестное поле формы")
			return
		}
	}
	if !fileSeen || password == "" {
		api.WriteError(w, 400, "bad_request", "выберите архив и введите пароль")
		return
	}
	var out backup.Preview
	if err := e.Core.Call(r.Context(), "imports.inspect", backup.ImportInspect{ID: id, Password: password}, &out); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "bot.import.inspect", "", nil)
	w.Header().Set("Cache-Control", "no-store")
	api.WriteJSON(w, 200, out)
	success = true
}

func (e *Exports) restoreMetrics(ctx context.Context, preview backup.Preview, id int64) error {
	if !preview.Metrics {
		return nil
	}
	reader := &backup.RemoteReader{Ctx: ctx, Ref: backup.FileRef{ID: preview.ID}, Size: preview.Manifest.Entries["metrics/history.json"].Size, ReadChunk: func(ctx context.Context, ref backup.FileRef) ([]byte, error) {
		var chunk []byte
		err := e.Core.Call(ctx, "imports.read", ref, &chunk)
		return chunk, err
	}}
	// Finish socket IO before taking panel.db's write transaction.
	file, err := e.Extras.Create(ctx, id, "restore-metrics.json", func(w io.Writer) error { _, err := io.Copy(w, reader); return err })
	if err != nil {
		return err
	}
	ref := backup.FileRef{ID: file.ID, BotID: id}
	defer e.Extras.Delete(ref)
	f, err := e.Extras.Open(ref)
	if err != nil {
		return err
	}
	defer f.Close()
	return e.Metrics.DB.Tx(ctx, func(tx *sql.Tx) error {
		sections := map[string]func(map[string]any) error{}
		for _, table := range []string{"m_minute", "m_hour", "m_day", "m_uniq"} {
			restorer, err := backup.NewTableRestorer(ctx, tx, table)
			if err != nil {
				return err
			}
			sections[table] = func(row map[string]any) error { row["bot_id"] = id; return restorer.Insert(row, true) }
		}
		return backup.ReadRowsDocument(f, sections)
	})
}

func (e *Exports) restoreProfile(ctx context.Context, id int64, p botconfig.Profile) error {
	langs := map[string]bool{}
	for key := range p.Name {
		langs[key] = true
	}
	for key := range p.Description {
		langs[key] = true
	}
	for key := range p.ShortDescription {
		langs[key] = true
	}
	for key := range p.Commands {
		langs[key] = true
	}
	if p.Menu != nil {
		langs["default"] = true
	}
	ordered := []string{}
	for lang := range langs {
		ordered = append(ordered, lang)
	}
	sort.Strings(ordered)
	for _, lang := range ordered {
		req := core.ProfileReq{BotID: id, Lang: lang, Actor: api.Actor(ctx)}
		if lang == "default" {
			req.Lang = ""
		}
		var current core.ProfileView
		if err := e.Core.Call(ctx, "profile.get", req, &current); err != nil {
			return err
		}
		if value, ok := p.Name[lang]; ok && value != current.Profile.Name {
			req.Name = &value
		}
		if value, ok := p.Description[lang]; ok && value != current.Profile.Description {
			req.Description = &value
		}
		if value, ok := p.ShortDescription[lang]; ok && value != current.Profile.ShortDescription {
			req.ShortDescription = &value
		}
		if value, ok := p.Commands[lang]; ok && !reflect.DeepEqual(value, current.Profile.Commands) {
			req.Commands = &value
		}
		if lang == "default" && p.Menu != nil && !reflect.DeepEqual(p.Menu, &current.Menu) {
			req.Menu = p.Menu
		}
		if req.Name == nil && req.Description == nil && req.ShortDescription == nil && req.Commands == nil && req.Menu == nil {
			continue
		}
		var saved core.ProfileSaved
		if err := e.Core.Call(ctx, "profile.set", req, &saved); err != nil {
			return err
		}
		if len(saved.Errors) > 0 {
			messages := []string{}
			for field, err := range saved.Errors {
				messages = append(messages, lang+"."+field+": "+err.Message)
			}
			sort.Strings(messages)
			return fmt.Errorf("%s", strings.Join(messages, "; "))
		}
	}
	return nil
}

func (e *Exports) applyImport(w http.ResponseWriter, r *http.Request) {
	var in importEdit
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
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
	id := r.PathValue("import")
	var preview backup.Preview
	if err := e.Core.Call(r.Context(), "imports.preview", id, &preview); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	request := backup.ImportApply{ID: id, Name: in.Name, Token: in.Token, Secrets: map[string]backup.SecretInput{}, Actor: api.Actor(r.Context())}
	for name, value := range in.Secrets {
		raw := []byte(value.Value)
		if value.Kind == secrets.KindFile {
			var err error
			raw, err = base64.StdEncoding.DecodeString(value.Value)
			if err != nil {
				api.WriteError(w, 400, "bad_request", "неверное содержимое файла секрета")
				return
			}
		}
		request.Secrets[name] = backup.SecretInput{Kind: value.Kind, Value: raw}
	}
	out := Imported{Warnings: []string{}}
	if err := e.Core.Call(r.Context(), "imports.apply", request, &out.Bot); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "bot.import", fmt.Sprint(out.Bot.ID), nil)
	rules := []alerts.Rule{}
	for _, rule := range preview.Document.Alerts {
		rules = append(rules, alerts.Rule{BotID: out.Bot.ID, Metric: rule.Metric, Op: rule.Op, Value: rule.Value, Minutes: rule.Minutes, Enabled: rule.Enabled})
	}
	if err := e.Documents.Rules.Replace(r.Context(), out.Bot.ID, rules); err != nil {
		out.Warnings = append(out.Warnings, "Пороги уведомлений: "+err.Error())
	}
	if e.Groups != nil && preview.Manifest.Group != nil && preview.Manifest.Group.Name != "" {
		groups, err := e.Groups.List(r.Context())
		groupID := int64(0)
		for _, g := range groups {
			if g.Name == preview.Manifest.Group.Name {
				groupID = g.ID
			}
		}
		if err == nil && groupID == 0 {
			g, createErr := e.Groups.Create(r.Context(), preview.Manifest.Group.Name)
			err = createErr
			groupID = g.ID
		}
		if err == nil {
			err = e.Groups.SetGroup(r.Context(), out.Bot.ID, groupID)
		}
		if err != nil {
			out.Warnings = append(out.Warnings, "Группа: "+err.Error())
		}
	}
	if err := e.restoreMetrics(r.Context(), preview, out.Bot.ID); err != nil {
		out.Warnings = append(out.Warnings, "Метрики: "+err.Error())
	}
	if len(out.Warnings) == 0 {
		if err := e.restoreProfile(r.Context(), out.Bot.ID, preview.Document.Profile); err != nil {
			out.Warnings = append(out.Warnings, "Профиль Telegram: "+err.Error())
		}
	}
	if in.Start && len(out.Warnings) == 0 {
		if out.Bot.State == core.StateRunning {
			out.Started = true
		} else if err := e.Core.Call(r.Context(), "bots.start", core.BotRef{BotID: out.Bot.ID, Actor: api.Actor(r.Context())}, nil); err != nil {
			out.Warnings = append(out.Warnings, "Бот импортирован, запуск: "+err.Error())
		} else {
			out.Started = true
		}
	}
	if len(out.Warnings) == 0 {
		e.abort(r.Context(), id)
	}
	e.Core.Call(r.Context(), "bots.get", out.Bot.ID, &out.Bot)
	w.Header().Set("Cache-Control", "no-store")
	api.WriteJSON(w, http.StatusCreated, out)
}
func (e *Exports) registerImports(mux *api.Mux) {
	mux.HandleFunc("POST /api/imports/bots", e.uploadImport)
	mux.HandleFunc("POST /api/imports/bots/{import}/apply", e.applyImport)
	mux.HandleFunc("DELETE /api/imports/bots/{import}", func(w http.ResponseWriter, r *http.Request) {
		e.abort(r.Context(), r.PathValue("import"))
		w.WriteHeader(http.StatusNoContent)
	})
}
