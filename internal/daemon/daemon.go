// Package daemon runs every registered project from one process: a
// supervisor per project, each polling Linear and taking tickets through
// the pipeline, with panics contained to the project that hit them.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/galimru/gosha/internal/agent"
	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/flock"
	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/pipeline"
)

// Defaults for the Daemon's intervals.
const (
	DefaultPoll      = 30 * time.Second
	DefaultRescan    = time.Minute
	DefaultCrashWait = 5 * time.Minute
)

// PassFunc does one pickup pass for a project (pipeline.Env.Pass).
type PassFunc func(ctx context.Context, project, only string) (handled bool, err error)

// Daemon supervises projects.
type Daemon struct {
	Home   config.Home
	Pass   PassFunc
	Notify notify.Notifier
	Log    *slog.Logger
	// Redact scrubs secrets from error text before it's logged or sent.
	Redact func(string) string
	// HeartbeatURL, if set, is pinged every Poll while the daemon runs.
	HeartbeatURL string
	// Limit is the usage-limit pause shared by every project: no pass
	// starts while it lasts.
	Limit interface{ Until() time.Time }
	// Jobs run alongside the supervisors until the daemon stops (the daily
	// digest, cleanup). Each must return once its context ends.
	Jobs []func(context.Context)

	// Poll is how often an idle project looks for Ready tickets, Rescan how
	// often projects/ is checked for added or removed projects, and
	// CrashWait how long a project waits after a panic. Zero means the
	// default.
	Poll, Rescan, CrashWait time.Duration
}

func (d *Daemon) poll() time.Duration      { return or(d.Poll, DefaultPoll) }
func (d *Daemon) rescan() time.Duration    { return or(d.Rescan, DefaultRescan) }
func (d *Daemon) crashWait() time.Duration { return or(d.CrashWait, DefaultCrashWait) }

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (d *Daemon) redact(s string) string {
	if d.Redact == nil {
		return s
	}
	return d.Redact(s)
}

// Run supervises every registered project until ctx ends, starting
// supervisors for projects added later and stopping those of projects
// removed. It returns once every supervisor has stopped.
func (d *Daemon) Run(ctx context.Context) error {
	d.Log.Info("daemon started", "poll", d.poll())
	go d.Heartbeat(ctx)
	var wg sync.WaitGroup
	for _, job := range d.Jobs {
		wg.Go(func() { job(ctx) })
	}
	type sup struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	running := map[string]*sup{}
	tick := time.NewTicker(d.rescan())
	defer tick.Stop()
	for {
		names, err := d.Home.Projects()
		if err != nil {
			d.Log.Error("list projects", "err", err)
		}
		want := map[string]bool{}
		for _, name := range names {
			want[name] = true
			if s, ok := running[name]; ok {
				select {
				case <-s.done: // exited (e.g. locked by another process): retry
				default:
					continue
				}
			}
			sctx, cancel := context.WithCancel(ctx)
			s := &sup{cancel: cancel, done: make(chan struct{})}
			running[name] = s
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer close(s.done)
				if err := d.Supervise(sctx, name, ""); err != nil {
					d.Log.Error("supervisor stopped", "project", name, "err", d.redact(err.Error()))
				}
			}()
		}
		for name, s := range running {
			if !want[name] {
				d.Log.Info("project removed, stopping", "project", name)
				s.cancel()
				delete(running, name)
			}
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			d.Log.Info("daemon stopped; interrupted tickets continue on the next start")
			return nil
		case <-tick.C:
		}
	}
}

// Heartbeat pings HeartbeatURL every poll interval until ctx ends.
func (d *Daemon) Heartbeat(ctx context.Context) {
	if d.HeartbeatURL == "" {
		return
	}
	tick := time.NewTicker(d.poll())
	defer tick.Stop()
	for {
		if err := notify.Ping(ctx, d.HeartbeatURL); err != nil && ctx.Err() == nil {
			d.Log.Warn("heartbeat", "err", d.redact(err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Supervise runs one project's poll loop until ctx ends: a pass every poll
// interval, straight away again after a pass that handled a ticket. It
// holds the project's run lock, so only one process runs a project.
func (d *Daemon) Supervise(ctx context.Context, name, only string) error {
	unlock, err := LockProject(d.Home, name)
	if err != nil {
		return err
	}
	defer unlock()
	log := d.Log.With("project", name)
	log.Info("supervising")
	crashed := false
	for {
		if err := d.waitLimit(ctx); err != nil {
			log.Info("stopped")
			return nil
		}
		handled, err := d.SafePass(ctx, name, only)
		wait := d.poll()
		var crash *CrashError
		var pe *pipeline.PausedError
		switch {
		case ctx.Err() != nil:
			log.Info("stopped")
			return nil
		case errors.As(err, &crash):
			if !crashed {
				d.Notify.Send(notify.Event{Project: name, Text: fmt.Sprintf("💥 crashed: %v · retrying every %s", d.redact(fmt.Sprint(crash.Panic)), d.crashWait())})
			}
			crashed = true
			wait = d.crashWait()
		case errors.As(err, new(*agent.LimitError)) && d.Limit != nil && !d.Limit.Until().IsZero():
			continue // waits for the limit to reset
		case errors.As(err, &pe):
			log.Warn("project paused", "reason", d.redact(pe.Err.Error()))
		case err != nil:
			log.Error("pass failed", "err", d.redact(err.Error()))
		default:
			crashed = false
			if handled {
				continue
			}
		}
		select {
		case <-ctx.Done():
			log.Info("stopped")
			return nil
		case <-time.After(wait):
		}
	}
}

// waitLimit waits while the usage-limit pause lasts.
func (d *Daemon) waitLimit(ctx context.Context) error {
	for d.Limit != nil {
		until := d.Limit.Until()
		if until.IsZero() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(until) + time.Second):
		}
	}
	return nil
}

// CrashError is a pass that panicked.
type CrashError struct {
	Panic any
	Stack []byte
}

func (e *CrashError) Error() string { return fmt.Sprintf("crashed: %v\n%s", e.Panic, e.Stack) }

// SafePass runs one pass, turning a panic into a CrashError so it stays
// within this project.
func (d *Daemon) SafePass(ctx context.Context, name, only string) (handled bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &CrashError{Panic: r, Stack: debug.Stack()}
			d.Log.Error("crashed", "project", name, "panic", d.redact(fmt.Sprint(r)), "stack", d.redact(string(err.(*CrashError).Stack)))
		}
	}()
	return d.Pass(ctx, name, only)
}

// LockProject takes the project's run lock, failing at once if another
// process (the daemon or gosha run) holds it.
func LockProject(home config.Home, name string) (unlock func(), err error) {
	unlock, err = flock.TryLock(home.Project(name).RunLock())
	if errors.Is(err, flock.ErrLocked) {
		return nil, fmt.Errorf("project %s is being run by another gosha process (the daemon?)", name)
	}
	return unlock, err
}
