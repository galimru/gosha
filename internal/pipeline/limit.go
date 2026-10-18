package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/store"
)

// limitWait is how long a usage limit pauses when Claude didn't say when it
// resets.
const limitWait = 15 * time.Minute

// metaUsageLimit is the store key holding the pause, so a restart keeps it.
const metaUsageLimit = "usage_limit_until"

// UsageLimit pauses every agent pass, across projects, while Claude's
// usage limit lasts: the Claude account is shared. A nil UsageLimit never
// pauses.
type UsageLimit struct {
	store  *store.Store
	notify notify.Notifier
	log    *slog.Logger

	mu    sync.Mutex
	until time.Time
	// cleared is closed when the current pause ends.
	cleared chan struct{}
	timer   *time.Timer
}

// NewUsageLimit restores a pause recorded before a restart.
func NewUsageLimit(ctx context.Context, st *store.Store, n notify.Notifier, log *slog.Logger) (*UsageLimit, error) {
	l := &UsageLimit{store: st, notify: n, log: log}
	v, err := st.Meta(ctx, metaUsageLimit)
	if err != nil {
		return nil, err
	}
	if until, err := time.Parse(time.RFC3339Nano, v); err == nil && time.Until(until) > 0 {
		l.mu.Lock()
		l.pause(until)
		l.mu.Unlock()
		log.Warn("usage limit still in effect", "until", until.Local().Format("15:04"))
	}
	return l, nil
}

// LimitedUntil reads the pause recorded in the store, for gosha status.
func LimitedUntil(ctx context.Context, st *store.Store) (time.Time, error) {
	v, err := st.Meta(ctx, metaUsageLimit)
	if err != nil || v == "" {
		return time.Time{}, err
	}
	until, _ := time.Parse(time.RFC3339Nano, v)
	if time.Until(until) <= 0 {
		return time.Time{}, nil
	}
	return until, nil
}

// Hit starts (or extends) the pause until resetsAt, or limitWait from now
// if it's unknown. Only the start of a pause is announced.
func (l *UsageLimit) Hit(ctx context.Context, resetsAt time.Time) {
	if l == nil {
		return
	}
	if time.Until(resetsAt) <= 0 {
		resetsAt = time.Now().Add(limitWait)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	active := l.cleared != nil
	if active && !resetsAt.After(l.until) {
		return
	}
	l.pause(resetsAt)
	if err := l.store.SetMeta(ctx, metaUsageLimit, resetsAt.UTC().Format(time.RFC3339Nano)); err != nil {
		l.log.Warn("record usage limit", "err", err)
	}
	if !active {
		l.log.Warn("usage limit: pausing all agents", "until", resetsAt.Local().Format("15:04"))
		l.notify.Send(notify.Event{Text: "⏸ Claude usage limit, resuming ~" + resetsAt.Local().Format("15:04")})
	}
}

// pause sets the pause end and its timer. l.mu is held.
func (l *UsageLimit) pause(until time.Time) {
	l.until = until
	if l.cleared == nil {
		l.cleared = make(chan struct{})
	}
	if l.timer != nil {
		l.timer.Stop()
	}
	l.timer = time.AfterFunc(time.Until(until), l.resume)
}

func (l *UsageLimit) resume() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cleared == nil || time.Until(l.until) > 0 {
		return // extended meanwhile
	}
	close(l.cleared)
	l.cleared, l.timer = nil, nil
	l.log.Info("usage limit reset: agents resume")
	l.notify.Send(notify.Event{Text: "▶️ usage limit reset · resumed"})
}

// Until is when the current pause ends, or zero.
func (l *UsageLimit) Until() time.Time {
	if l == nil {
		return time.Time{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cleared == nil {
		return time.Time{}
	}
	return l.until
}

// Wait returns once no pause is in effect, or when ctx ends.
func (l *UsageLimit) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	for {
		l.mu.Lock()
		ch := l.cleared
		l.mu.Unlock()
		if ch == nil {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}
