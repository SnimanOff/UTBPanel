package secrets

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// Redactor removes known secret values from log records. Every decrypted
// value is added to it, so a token that slips into an error message (for
// example, a Bot API URL) is printed as "***".
type Redactor struct {
	mu     sync.RWMutex
	values map[string]struct{}
	repl   *strings.Replacer
}

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor { return &Redactor{values: map[string]struct{}{}} }

// Add registers a secret value. Short values are ignored: replacing every
// "1" in the logs would hide everything and protect nothing.
func (r *Redactor) Add(v string) {
	if len(v) < 6 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.values[v]; ok {
		return
	}
	r.values[v] = struct{}{}
	pairs := make([]string, 0, 2*len(r.values))
	for s := range r.values {
		pairs = append(pairs, s, "***")
	}
	r.repl = strings.NewReplacer(pairs...)
}

// Redact replaces secret values in s.
func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	repl := r.repl
	r.mu.RUnlock()
	if repl == nil {
		return s
	}
	return repl.Replace(s)
}

// Handler wraps h so that every message and string attribute is redacted.
func (r *Redactor) Handler(h slog.Handler) slog.Handler { return &redactHandler{h: h, r: r} }

type redactHandler struct {
	h slog.Handler
	r *Redactor
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.h.Enabled(ctx, l) }

func (h *redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.h.Handle(ctx, out)
}

func (h *redactHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.r.Redact(v.String()))
	case slog.KindGroup:
		as := v.Group()
		out := make([]any, len(as))
		for i, x := range as {
			out[i] = h.attr(x)
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, h.r.Redact(err.Error()))
		}
		return slog.String(a.Key, h.r.Redact(v.String()))
	}
	return a
}

func (h *redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = h.attr(a)
	}
	return &redactHandler{h: h.h.WithAttrs(out), r: h.r}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{h: h.h.WithGroup(name), r: h.r}
}
