package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/tmpl"
)

// merge merges the PR at the head the checks passed on (DESIGN §8.5). A
// base that moved is merged into the branch, which then needs checks
// again.
func (t *ticket) merge(ctx context.Context) error {
	if err := t.save(ctx, StepMerge); err != nil {
		return err
	}
	tok, err := t.prs.Token(ctx)
	if err != nil {
		return err
	}
	if err := t.p.Repo.Fetch(ctx, tok); err != nil {
		return err
	}
	current, err := t.p.Repo.IsAncestor(ctx, "refs/remotes/origin/"+t.run.BaseBranch, "refs/heads/"+t.run.Branch)
	if err != nil {
		return err
	}
	if !current {
		return t.syncBase(ctx)
	}
	title := fmt.Sprintf("%s: %s (#%d)", t.issue.Identifier, t.issue.Title, t.run.PRNumber)
	sha, err := t.prs.Merge(ctx, t.run.PRNumber, t.run.CheckedSHA, t.p.Config.GitHub.MergeMethod, title, "Ticket: "+t.issue.URL)
	switch {
	case errors.Is(err, github.ErrHeadMoved):
		return t.block(ctx, fmt.Sprintf("Someone else pushed to `%s` after its checks passed, so Gosha didn't merge it. "+
			"Gosha only merges what it checked. Close the PR and move the ticket to Ready to start over, or merge the PR yourself.", t.run.Branch), nil)
	case errors.Is(err, github.ErrNotMergeable):
		return t.block(ctx, "GitHub refused to merge the pull request: "+err.Error(), nil)
	case err != nil:
		return err
	}
	t.run.MergeSHA = sha
	if err := t.save(ctx, StepMerge); err != nil {
		return err
	}
	t.e.Log.Info("merged", "project", t.p.Name, "ticket", t.issue.Identifier, "pr", t.run.PRURL, "sha", sha)
	t.notify(fmt.Sprintf("%s · %s-merged %.7s", t.issue.Identifier, t.p.Config.GitHub.MergeMethod, sha), t.prLink())
	return t.afterMerge(ctx)
}

// syncBase has GitHub merge the moved base into the branch (DESIGN §8.5).
// A clean merge is fetched and checked again; conflicts go to the
// implementer as a check round.
func (t *ticket) syncBase(ctx context.Context) error {
	if err := t.save(ctx, StepSync); err != nil {
		return err
	}
	head, err := t.p.Repo.RevParse(ctx, "refs/heads/"+t.run.Branch)
	if err != nil {
		return err
	}
	err = t.prs.UpdateBranch(ctx, t.run.PRNumber, head)
	switch {
	case errors.Is(err, github.ErrHeadMoved):
		return t.block(ctx, fmt.Sprintf("Someone else pushed to `%s` after its checks passed, so Gosha didn't update it with `%s`. "+
			"Close the PR and move the ticket to Ready to start over, or merge the PR yourself.", t.run.Branch, t.run.BaseBranch), nil)
	case errors.Is(err, github.ErrConflict):
		return t.conflicts(ctx, head)
	case err != nil:
		return err
	}
	if err := t.waitHead(ctx, head); err != nil {
		return err
	}
	tok, err := t.prs.Token(ctx)
	if err != nil {
		return err
	}
	if err := t.p.Repo.Fetch(ctx, tok); err != nil {
		return err
	}
	if err := t.p.Repo.TakeOrigin(ctx, t.run.Branch); err != nil {
		return err
	}
	// The checkout doesn't have the merge commit: rebuild it next time, or
	// the implementer's next commits would drop it.
	if err := os.RemoveAll(t.checkout()); err != nil {
		return err
	}
	t.notify(fmt.Sprintf("%s · merged %s into the branch · checking again", t.issue.Identifier, t.run.BaseBranch), t.prLink())
	return t.checks(ctx)
}

// waitHead waits for GitHub to move the PR's head off head, which it does
// once a branch update is done.
func (t *ticket) waitHead(ctx context.Context, head string) error {
	deadline := time.Now().Add(t.p.Config.Checks.Timeout)
	for {
		pr, err := t.prs.PR(ctx, t.run.PRNumber)
		if err != nil {
			return err
		}
		if pr.HeadSHA != "" && pr.HeadSHA != head {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("GitHub didn't update the pull request with `%s` within %v", t.run.BaseBranch, t.p.Config.Checks.Timeout)
		}
		if err := sleep(ctx, checkPoll); err != nil {
			return err
		}
	}
}

// conflicts sends a base that doesn't merge cleanly to the implementer, as
// a check round.
func (t *ticket) conflicts(ctx context.Context, head string) error {
	limit := t.p.Config.Checks.MaxRounds
	if t.run.CheckRound >= limit {
		return t.block(ctx, fmt.Sprintf("`%s` conflicts with this branch, and there are no fix rounds left (`checks.max_rounds` %d).",
			t.run.BaseBranch, limit), nil)
	}
	fb := tmpl.Feedback{Conflicts: true}
	if err := t.recordFailure(ctx, head, fb); err != nil {
		return err
	}
	t.notify(fmt.Sprintf("%s · conflicts with %s → resolving %d/%d", t.issue.Identifier, t.run.BaseBranch, t.run.CheckRound, limit), t.prLink())
	return t.fix(ctx, fb)
}
