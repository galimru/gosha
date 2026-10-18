package pipeline

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"time"

	"github.com/galimru/gosha/internal/linear"
	"github.com/galimru/gosha/internal/store"
)

// OutcomeCanceled is a run whose ticket a human moved to Canceled.
const OutcomeCanceled = "canceled"

// watchEvery is how often an active ticket's state is re-read; swapped in
// tests.
var watchEvery = 30 * time.Second

// errTicketMoved cancels a pickup whose ticket a human moved out of In
// Progress.
var errTicketMoved = errors.New("the ticket was moved out of In Progress")

// mover is the shared state between a pickup and its watcher.
type mover struct {
	// leaving is set before Gosha moves the ticket itself (Blocked, In
	// Review), so the watcher doesn't take that for a human's move.
	leaving atomic.Bool
	// to is the state a human moved the ticket to.
	to atomic.Pointer[linear.State]
}

// watch re-reads the ticket every watchEvery until ctx ends, and cancels
// ctx with errTicketMoved once a human has moved it out of In Progress.
// stop ends the watcher and waits for it.
func (t *ticket) watch(ctx context.Context, cancel context.CancelCauseFunc) (stop func()) {
	done := make(chan struct{})
	quit := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(watchEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-quit:
				return
			case <-tick.C:
			}
			if t.moved.leaving.Load() {
				continue
			}
			is, err := t.issues.Issue(ctx, t.issue.ID)
			if err != nil {
				if ctx.Err() == nil {
					t.e.Log.Warn("re-read ticket", "project", t.p.Name, "ticket", t.issue.Identifier, "err", err)
				}
				continue
			}
			// Checked again after the read: Gosha may have started moving it.
			if is.State.ID == t.states.InProgress.ID || t.moved.leaving.Load() {
				continue
			}
			t.moved.to.Store(&is.State)
			cancel(errTicketMoved)
			return
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}

// leave marks that Gosha is about to move the ticket out of In Progress.
func (t *ticket) leave() { t.moved.leaving.Store(true) }

// stopped finishes a pickup a human stopped by moving the ticket. Canceled
// abandons the run; any other state pauses it with everything kept, and
// Ready later resumes it.
func (t *ticket) stopped(ctx context.Context) error {
	to := t.moved.to.Load()
	if to == nil || !to.IsCanceled() {
		name := ""
		if to != nil {
			name = to.Name
		}
		t.e.Log.Info("paused: the ticket was moved", "project", t.p.Name, "ticket", t.issue.Identifier, "to", name, "step", t.run.Step)
		return nil
	}
	return t.e.cancelRun(ctx, t.p, t.prs, t.run)
}

// cancelRun ends a run whose ticket was canceled: it closes the PR, deletes
// the branch and removes the workspace.
func (e *Env) cancelRun(ctx context.Context, p *Project, prs prOps, r *store.Run) error {
	if r.PRNumber != 0 {
		pr, err := prs.PR(ctx, r.PRNumber)
		if err != nil {
			return err
		}
		if pr.State == "open" {
			if err := prs.ClosePR(ctx, r.PRNumber); err != nil {
				return err
			}
		}
	}
	if r.Branch != "" && r.MergeSHA == "" {
		tok, err := prs.Token(ctx)
		if err != nil {
			return err
		}
		if err := p.Repo.DeleteBranch(ctx, tok, r.Branch); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(e.Home.Workspace(p.Name, r.TicketID).Dir); err != nil {
		return err
	}
	if err := e.Store.FinishRun(ctx, r.ID, OutcomeCanceled); err != nil {
		return err
	}
	e.Log.Info("canceled: PR closed, branch deleted", "project", p.Name, "ticket", r.TicketID)
	return nil
}

// reconcileCanceled ends the project's open runs, parked ones included,
// whose ticket was canceled while Gosha wasn't watching it (Blocked, or
// while Gosha was down).
func (e *Env) reconcileCanceled(ctx context.Context, p *Project, issues interface {
	Issue(ctx context.Context, id string) (*linear.Issue, error)
}, prs prOps) error {
	runs, err := e.Store.OpenRuns(ctx)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Project != p.Name {
			continue
		}
		is, err := issues.Issue(ctx, r.TicketID)
		if err != nil {
			return err
		}
		if !is.State.IsCanceled() {
			continue
		}
		if err := e.cancelRun(ctx, p, prs, r); err != nil {
			return err
		}
	}
	return nil
}
