package config

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// minSecretLen keeps short values (a "1" or "main") from being scrubbed
// out of every message.
const minSecretLen = 6

// Redactor scrubs resolved secret values from text (DESIGN §5.4).
// The zero value is not usable; a nil *Redactor redacts nothing.
type Redactor struct {
	mu     sync.RWMutex
	values []string
	r      *strings.Replacer
}

func NewRedactor() *Redactor { return &Redactor{} }

// Add registers a secret value.
func (r *Redactor) Add(v string) {
	if r == nil || len(v) < minSecretLen {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, have := range r.values {
		if have == v {
			return
		}
	}
	r.values = append(r.values, v)
	// Longest first, so a secret that contains another is replaced whole.
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	pairs := make([]string, 0, 2*len(r.values))
	for _, s := range r.values {
		pairs = append(pairs, s, "***")
	}
	r.r = strings.NewReplacer(pairs...)
}

// Redact returns s with every registered value replaced by ***.
func (r *Redactor) Redact(s string) string {
	if r == nil {
		return s
	}
	r.mu.RLock()
	rep := r.r
	r.mu.RUnlock()
	if rep == nil {
		return s
	}
	return rep.Replace(s)
}

// Handler wraps next so that messages and string attributes are redacted.
func (r *Redactor) Handler(next slog.Handler) slog.Handler {
	return &redactHandler{next: next, red: r}
}

type redactHandler struct {
	next slog.Handler
	red  *Redactor
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.red.Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *redactHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.red.Redact(v.String()))
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]any, len(attrs))
		for i, g := range attrs {
			out[i] = h.attr(g)
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, h.red.Redact(err.Error()))
		}
	}
	return a
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = h.attr(a)
	}
	return &redactHandler{next: h.next.WithAttrs(out), red: h.red}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{next: h.next.WithGroup(name), red: h.red}
}
