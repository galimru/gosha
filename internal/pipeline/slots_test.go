package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/config"
)

func TestAgentSlotsAreShared(t *testing.T) {
	e := &Env{Global: &config.Global{Concurrency: 1}}
	release, err := e.acquireSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := e.acquireSlot(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second slot = %v, want to wait until the deadline", err)
	}
	release()
	release, err = e.acquireSlot(context.Background())
	if err != nil {
		t.Fatalf("slot after release: %v", err)
	}
	release()
}
