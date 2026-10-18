package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/gitx"
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
	diff, err := t.p.Repo.Diff(ctx, t.run.BaseBranch, t.run.Branch)
	if err != nil {
		return err
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
	t.report.Diff = diff.String()
	if err := t.save(ctx, StepMerge); err != nil {
		return err
	}
	t.e.Log.Info("merged", "project", t.p.Name, "ticket", t.issue.Identifier, "pr", t.run.PRURL, "sha", sha)
	t.notify(fmt.Sprintf("%s · %s-merged %.7s", t.issue.Identifier, t.p.Config.GitHub.MergeMethod, sha), t.prLink())
	return t.afterMerge(ctx)
}

// syncBase merges the moved base into the branch in the bare clone. A
// clean merge is pushed and checked again; conflicts go to the implementer
// as a check round (DESIGN §8.5).
func (t *ticket) syncBase(ctx context.Context) error {
	if err := t.save(ctx, StepSync); err != nil {
		return err
	}
	head, err := t.p.Repo.RevParse(ctx, "refs/heads/"+t.run.Branch)
	if err != nil {
		return err
	}
	conflicts, err := t.p.Repo.MergeBase(ctx, t.run.BaseBranch, t.run.Branch, gitx.Author{Name: t.bot.Name, Email: t.bot.Email})
	if err != nil {
		return err
	}
	if len(conflicts) == 0 {
		if ok, err := t.push(ctx); !ok {
			return err
		}
		// The checkout doesn't have the merge commit: rebuild it next time,
		// or the implementer's next commits would drop it.
		if err := os.RemoveAll(t.checkout()); err != nil {
			return err
		}
		t.notify(fmt.Sprintf("%s · merged %s into the branch · checking again", t.issue.Identifier, t.run.BaseBranch), t.prLink())
		return t.checks(ctx)
	}
	limit := t.p.Config.Checks.MaxRounds
	if t.run.CheckRound >= limit {
		return t.block(ctx, fmt.Sprintf("`%s` conflicts with this branch in %s, and there are no fix rounds left (`checks.max_rounds` %d).",
			t.run.BaseBranch, codeList(conflicts), limit), nil)
	}
	var list strings.Builder
	for _, c := range conflicts {
		list.WriteString("- `" + c + "`\n")
	}
	fb := tmpl.Feedback{Conflicts: strings.TrimSpace(list.String())}
	t.report.Fixes = append(t.report.Fixes, fmt.Sprintf("Conflicts with `%s` in %d files → resolved by the implementer", t.run.BaseBranch, len(conflicts)))
	if err := t.recordFailure(ctx, head, fb); err != nil {
		return err
	}
	t.notify(fmt.Sprintf("%s · conflicts with %s in %d files → resolving %d/%d", t.issue.Identifier, t.run.BaseBranch, len(conflicts), t.run.CheckRound, limit), t.prLink())
	return t.fix(ctx, fb)
}
