package pipeline

import (
	"os"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/linear"
)

var backlog = linear.State{ID: "s-backlog", Name: "Backlog", Type: linear.TypeBacklog}

// movedWhileChecking opens a PR, then has a human move the ticket to "to"
// while Gosha waits for its checks.
func movedWhileChecking(t *testing.T, to linear.State) *harness {
	t.Helper()
	defer func(d time.Duration) { watchEvery = d }(watchEvery)
	watchEvery = time.Millisecond
	h := newHarness(t, "")
	h.agent.act = func(_ int, dir string) string { commitFile(t, dir, "login.go"); return doneResult }
	h.prs.checks = func(call int, _ string) []github.Check {
		if call == 2 {
			h.lin.moved.Store(&to)
		}
		return []github.Check{{Name: "test", Kind: github.KindCheckRun, Status: "in_progress"}}
	}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if h.lin.state() != states.InProgress.ID || len(h.lin.comments) != 0 {
		t.Errorf("Gosha changed the ticket: %+v", h.lin)
	}
	if ev := h.events.take(); len(ev) != 0 {
		t.Errorf("events = %q", ev)
	}
	return h
}

func TestCanceledClosesPRAndDeletesBranch(t *testing.T) {
	h := movedWhileChecking(t, canceled)
	r := h.lastRun()
	if r.Outcome != OutcomeCanceled {
		t.Errorf("outcome = %q", r.Outcome)
	}
	if h.prs.prs[1].State != "closed" {
		t.Errorf("PR = %+v", h.prs.prs[1])
	}
	if h.onUpstream(r.Branch) {
		t.Error("branch not deleted")
	}
	if _, err := os.Stat(h.env.Home.Workspace("demo", "NXT-12").Dir); !os.IsNotExist(err) {
		t.Errorf("workspace left behind: %v", err)
	}
}

func TestPausedKeepsEverything(t *testing.T) {
	h := movedWhileChecking(t, backlog)
	r := h.run()
	if r.Step != StepChecks || h.prs.prs[1].State != "open" || !h.onUpstream(r.Branch) {
		t.Errorf("run = %+v, PR = %+v", r, h.prs.prs[1])
	}
}

func TestReconcileCanceledParkedRun(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(_ int, dir string) string {
		commitFile(t, dir, "partial.go")
		return `{"status":"blocked","blocked_reason":"Which provider?","questions":["?"]}`
	}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if r := h.run(); !Parked(r) {
		t.Fatalf("run = %+v, want Blocked", r)
	}
	// A human cancels the Blocked ticket; the next pass cleans up.
	h.lin.moved.Store(&canceled)
	if err := h.env.reconcileCanceled(h.ctx, h.p, h.lin, h.prs); err != nil {
		t.Fatal(err)
	}
	if r := h.lastRun(); r.Outcome != OutcomeCanceled {
		t.Errorf("run = %+v", r)
	}
	if _, err := os.Stat(h.env.Home.Workspace("demo", "NXT-12").Dir); !os.IsNotExist(err) {
		t.Errorf("workspace left behind: %v", err)
	}
}
