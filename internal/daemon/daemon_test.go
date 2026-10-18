package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/notify"
)

type events struct {
	mu   sync.Mutex
	sent []string
}

func (e *events) Send(ev notify.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sent = append(e.sent, ev.Project+": "+ev.Text)
}

func (e *events) Flush(context.Context) error { return nil }

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.sent...)
}

// counter counts passes per project.
type counter struct {
	mu     sync.Mutex
	passes map[string]int
}

func (c *counter) inc(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.passes == nil {
		c.passes = map[string]int{}
	}
	c.passes[name]++
	return c.passes[name]
}

func (c *counter) get(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.passes[name]
}

func addProject(t *testing.T, home config.Home, name string) {
	t.Helper()
	if err := os.MkdirAll(home.Project(name).Repo(), 0o700); err != nil {
		t.Fatal(err)
	}
}

func newDaemon(home config.Home, pass PassFunc, ev *events) *Daemon {
	return &Daemon{Home: home, Pass: pass, Notify: ev, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Poll: 5 * time.Millisecond, Rescan: 20 * time.Millisecond, CrashWait: 10 * time.Millisecond}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPanicStaysInItsProject(t *testing.T) {
	home := config.Home(t.TempDir())
	addProject(t, home, "a")
	addProject(t, home, "b")
	var c counter
	ev := &events{}
	d := newDaemon(home, func(ctx context.Context, name, _ string) (bool, error) {
		c.inc(name)
		if name == "a" {
			panic("boom")
		}
		return false, nil
	}, ev)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- d.Run(ctx) }()
	waitFor(t, "both projects polling", func() bool { return c.get("a") >= 3 && c.get("b") >= 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sent := ev.all()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "a: 💥 crashed: boom") {
		t.Errorf("alerts = %q, want one crash alert for a", sent)
	}
}

func TestCrashAlertsAgainAfterRecovery(t *testing.T) {
	home := config.Home(t.TempDir())
	addProject(t, home, "a")
	var c counter
	ev := &events{}
	d := newDaemon(home, func(ctx context.Context, name, _ string) (bool, error) {
		n := c.inc(name)
		if n == 1 || n == 2 || n == 4 { // crash, crash, ok, crash
			panic("boom")
		}
		return false, nil
	}, ev)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Supervise(ctx, "a", "")
	waitFor(t, "five passes", func() bool { return c.get("a") >= 5 })
	cancel()
	if n := len(ev.all()); n != 2 {
		t.Errorf("crash alerts = %d (%q), want 2", n, ev.all())
	}
}

func TestRescanStartsAndStops(t *testing.T) {
	home := config.Home(t.TempDir())
	addProject(t, home, "a")
	var c counter
	d := newDaemon(home, func(ctx context.Context, name, _ string) (bool, error) {
		c.inc(name)
		return false, nil
	}, &events{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- d.Run(ctx) }()
	waitFor(t, "a polling", func() bool { return c.get("a") > 0 })
	addProject(t, home, "b")
	waitFor(t, "b started", func() bool { return c.get("b") > 0 })
	if err := os.RemoveAll(home.Project("a").Dir); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond) // a rescan or two
	stopped := c.get("a")
	time.Sleep(60 * time.Millisecond)
	if c.get("a") != stopped {
		t.Errorf("removed project still polling")
	}
	cancel()
	<-done
}

func TestOneProcessPerProject(t *testing.T) {
	home := config.Home(t.TempDir())
	addProject(t, home, "a")
	unlock, err := LockProject(home, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	d := newDaemon(home, func(context.Context, string, string) (bool, error) { return false, nil }, &events{})
	err = d.Supervise(context.Background(), "a", "")
	if err == nil || !strings.Contains(err.Error(), "another gosha process") {
		t.Fatalf("Supervise = %v", err)
	}
}
