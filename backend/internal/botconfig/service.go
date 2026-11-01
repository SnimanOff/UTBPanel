package botconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

type Caller interface {
	Call(context.Context, string, any, any) error
}
type Service struct {
	Core       Caller
	Rules      *alerts.Rules
	Group      func(context.Context, int64) (*Group, error)
	WriteError func(http.ResponseWriter, error)
	Guard      Guard
}
type View struct {
	Document Document `json:"document"`
	YAML     string   `json:"yaml"`
}
type Edit struct {
	Version  string    `json:"version"`
	Document *Document `json:"document,omitempty"`
	YAML     *string   `json:"yaml,omitempty"`
}
type Result struct {
	View
	Applied        bool              `json:"applied"`
	Restart        bool              `json:"restart"`
	MissingSecrets []string          `json:"missing_secrets"`
	ProfileErrors  map[string]string `json:"profile_errors"`
}

func view(d Document) (View, error) { text, err := YAML(d); return View{Document: d, YAML: text}, err }
func (s *Service) snapshot(ctx context.Context, id int64) (Snapshot, error) {
	var out Snapshot
	if err := s.Core.Call(ctx, "config.get", id, &out); err != nil {
		return out, err
	}
	rules, err := s.Rules.List(ctx, id)
	if err != nil {
		return out, err
	}
	out.Document.Alerts = []Alert{}
	for _, r := range rules {
		out.Document.Alerts = append(out.Document.Alerts, Alert{Metric: r.Metric, Op: r.Op, Value: r.Value, Minutes: r.Minutes, Enabled: r.Enabled})
	}
	if s.Group != nil {
		out.Document.Group, err = s.Group(ctx, id)
		if err != nil {
			return out, err
		}
	}
	Normalize(&out.Document)
	out.Document.Version = Version(out.Version, out.Document)
	return out, nil
}
func (s *Service) Get(ctx context.Context, id int64) (View, error) {
	ctx, unlock, err := s.Guard.Lock(ctx)
	if err != nil {
		return View{}, err
	}
	defer unlock()
	snap, err := s.snapshot(ctx, id)
	if err != nil {
		return View{}, err
	}
	return view(snap.Document)
}

// ExportSnapshot retains the core revision alongside panel-owned settings.
func (s *Service) ExportSnapshot(ctx context.Context, id int64) (Snapshot, error) {
	ctx, unlock, err := s.Guard.Lock(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	return s.snapshot(ctx, id)
}
func (s *Service) prepare(ctx context.Context, id int64, in Edit) (Snapshot, Document, error) {
	var d Document
	if (in.Document == nil) == (in.YAML == nil) {
		return Snapshot{}, d, wire.Errorf(wire.CodeBadRequest, "укажите document или yaml")
	}
	if in.YAML != nil {
		var err error
		d, err = Decode([]byte(*in.YAML))
		if err != nil {
			return Snapshot{}, d, wire.Errorf(wire.CodeBadRequest, "%v", err)
		}
	} else {
		d = *in.Document
		Normalize(&d)
		if err := Validate(d); err != nil {
			return Snapshot{}, d, wire.Errorf(wire.CodeBadRequest, "%v", err)
		}
	}
	current, err := s.snapshot(ctx, id)
	if err != nil {
		return current, d, err
	}
	if in.Version == "" {
		in.Version = d.Version
	}
	if in.Version == "" || in.Version != current.Document.Version || (d.Version != "" && d.Version != in.Version) {
		return current, d, wire.Errorf("conflict", "настройки изменились; перечитайте документ")
	}
	if d.Group != nil && !reflect.DeepEqual(d.Group, current.Document.Group) {
		return current, d, wire.Errorf(wire.CodeBadRequest, "группа доступна только для чтения")
	}
	for _, r := range d.Alerts {
		if err := (alerts.Rule{Metric: r.Metric, Op: r.Op, Value: r.Value, Minutes: r.Minutes, Enabled: r.Enabled}).Validate(); err != nil {
			return current, d, wire.Errorf(wire.CodeBadRequest, "%v", err)
		}
	}
	d.Group = current.Document.Group
	return current, d, nil
}
func (s *Service) Diff(ctx context.Context, id int64, in Edit) (Difference, error) {
	ctx, unlock, err := s.Guard.Lock(ctx)
	if err != nil {
		return Difference{}, err
	}
	defer unlock()
	current, d, err := s.prepare(ctx, id, in)
	if err != nil {
		return Difference{}, err
	}
	return Diff(current.Document, d), nil
}
func (s *Service) Apply(ctx context.Context, id int64, in Edit, actorCtx context.Context) (Result, error) {
	var out Result
	ctx, unlock, err := s.Guard.Lock(ctx)
	if err != nil {
		return out, err
	}
	defer unlock()
	current, d, err := s.prepare(ctx, id, in)
	if err != nil {
		return out, err
	}
	var applied ApplyResult
	if err := s.Core.Call(ctx, "config.apply", ApplyRequest{BotID: id, Version: current.Version, Document: d, Actor: api.Actor(actorCtx)}, &applied); err != nil {
		return out, err
	}
	if applied.Applied && !reflect.DeepEqual(current.Document.Alerts, d.Alerts) {
		rules := []alerts.Rule{}
		for _, r := range d.Alerts {
			rules = append(rules, alerts.Rule{Metric: r.Metric, Op: r.Op, Value: r.Value, Minutes: r.Minutes, Enabled: r.Enabled})
		}
		if err := s.Rules.Replace(ctx, id, rules); err != nil {
			return out, fmt.Errorf("основные настройки применены, пороги уведомлений не сохранены: %w", err)
		}
	}
	// Read afresh: Telegram may have accepted only some profile fields.
	snap, err := s.snapshot(ctx, id)
	if err != nil {
		return out, err
	}
	out.View, err = view(snap.Document)
	out.Applied = applied.Applied
	out.Restart = applied.Restart
	out.MissingSecrets = applied.MissingSecrets
	out.ProfileErrors = applied.ProfileErrors
	return out, err
}

func (s *Service) Register(mux *api.Mux) {
	pathID := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			api.WriteError(w, 400, "bad_request", "invalid bot id")
			return 0, false
		}
		return id, true
	}
	mux.HandleFunc("GET /api/bots/{id}/config", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := s.Get(r.Context(), id)
		if err != nil {
			s.WriteError(w, err)
			return
		}
		api.WriteJSON(w, 200, out)
	})
	for _, apply := range []bool{false, true} {
		name := "diff"
		if apply {
			name = "apply"
		}
		mux.HandleFunc("POST /api/bots/{id}/config/"+name, func(w http.ResponseWriter, r *http.Request) {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			var in Edit
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
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
			var out any
			var err error
			if apply {
				audit.Annotate(r, "config.apply", "", nil)
				out, err = s.Apply(r.Context(), id, in, r.Context())
			} else {
				out, err = s.Diff(r.Context(), id, in)
			}
			if err != nil {
				s.WriteError(w, err)
				return
			}
			api.WriteJSON(w, 200, out)
		})
	}
	mux.HandleFunc("GET /api/bot-config/schema", func(w http.ResponseWriter, r *http.Request) {
		out, err := Schema()
		if err != nil {
			s.WriteError(w, err)
			return
		}
		api.WriteJSON(w, 200, out)
	})
}
