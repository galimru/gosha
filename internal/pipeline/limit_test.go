package pipeline

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/store"
)

func TestUsageLimitPausesEveryAgent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gosha.db")
	st, err := store.Open(ctx, path, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ev := &recorder{}
	l, err := NewUsageLimit(ctx, st, ev, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	e := &Env{Global: &config.Global{Concurrency: 2}, Limit: l}

	until := time.Now().Add(300 * time.Millisecond)
	l.Hit(ctx, until)
	l.Hit(ctx, until) // a second project hitting the same limit
	if got, _ := LimitedUntil(ctx, st); got.IsZero() {
		t.Error("pause not recorded")
	}
	// A restart restores the pause, without announcing it again.
	restored, err := NewUsageLimit(ctx, st, ev, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Until().IsZero() {
		t.Error("pause not restored")
	}

	start := time.Now()
	release, err := e.acquireSlot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Errorf("slot granted after %v, during the pause", waited)
	}
	time.Sleep(100 * time.Millisecond) // the restored limiter resumes too
	got := ev.take()
	if len(got) < 2 || got[0] != "⏸ Claude usage limit, resuming ~"+until.Local().Format("15:04") || got[1] != "▶️ usage limit reset · resumed" {
		t.Errorf("events = %q", got)
	}
}
