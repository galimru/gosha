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

// merge merges the PR at the head the checks passed on (DESIGN §8.5).
// New human PR feedback goes back to the implementer first, and a base
// that moved is merged into the branch, which then needs checks again.
func (t *ticket) merge(ctx context.Context) error {
	if err := t.save(ctx, StepMerge); err != nil {
		return err
	}
	fb, err := t.prs.HumanFeedback(ctx, t.run.PRNumber, t.run.PRCursor)
	if err != nil {
		return err
	}
	if len(fb) > 0 {
		t.notify(fmt.Sprintf("%s · %d new human PR comments before merging", t.issue.Identifier, len(fb)), t.prLink())
		return t.reviewLoop(ctx, true)
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
			"Gosha only merges what it checked. Comment `/restart` and move the ticket to Ready to start over, or merge the PR yourself.", t.run.Branch), nil)
	case errors.Is(err, github.ErrNotMergeable):
		return t.block(ctx, "GitHub refused to merge the pull request: "+err.Error(), nil)
	case err != nil:
		return err
	}
	t.run.MergeSHA = sha
	if err := t.save(ctx, StepMerge); err != nil {
		return err
	}
	t.diff = &diff
	t.e.Log.Info("merged", "project", t.p.Name, "ticket", t.issue.Identifier, "pr", t.run.PRURL, "sha", sha)
	t.notify(fmt.Sprintf("%s · %s-merged %.7s", t.issue.Identifier, t.p.Config.GitHub.MergeMethod, sha), t.prLink())
	return t.deliver(ctx)
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
	if err := t.recordFailure(ctx, head, fb); err != nil {
		return err
	}
	t.notify(fmt.Sprintf("%s · conflicts with %s in %d files → resolving %d/%d", t.issue.Identifier, t.run.BaseBranch, len(conflicts), t.run.CheckRound, limit), t.prLink())
	return t.fix(ctx, fb)
}

// deliver cleans up after the merge, reports on the ticket and moves it to
// In Review. Until M6 the report is short and there's no deploy watch.
func (t *ticket) deliver(ctx context.Context) error {
	tok, err := t.prs.Token(ctx)
	if err != nil {
		return err
	}
	if err := t.p.Repo.DeleteBranch(ctx, tok, t.run.Branch); err != nil {
		return err
	}
	if err := os.RemoveAll(t.checkout()); err != nil {
		return err
	}
	if _, err := t.issues.CreateComment(ctx, t.issue.ID, t.deliveredComment()); err != nil {
		return err
	}
	if err := t.issues.Transition(ctx, t.issue.ID, t.states.InReview.ID); err != nil {
		return err
	}
	if err := t.e.Store.FinishRun(ctx, t.run.ID, OutcomeDelivered); err != nil {
		return err
	}
	t.e.Log.Info("delivered", "project", t.p.Name, "ticket", t.issue.Identifier)
	t.notify(fmt.Sprintf("%s · ✅ delivered · In Review", t.issue.Identifier), t.ticketLink(), t.prLink())
	return nil
}

func (t *ticket) deliveredComment() string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Delivered:** [PR #%d](%s) is merged (`%.7s`)", t.run.PRNumber, t.run.PRURL, t.run.MergeSHA)
	if t.diff != nil {
		fmt.Fprintf(&b, " · %s", t.diff)
	}
	b.WriteString(".\n")
	if s := strings.TrimSpace(t.summary); s != "" {
		b.WriteString("\n" + t.e.Redactor.Redact(s) + "\n")
	}
	b.WriteString("\n**How it was checked**\n")
	fmt.Fprintf(&b, "- Review: %d round(s) by Gosha's reviewer", max(t.run.ReviewRound, 1))
	if t.run.CheckRound > 0 {
		fmt.Fprintf(&b, ", %d check fix round(s)", t.run.CheckRound)
	}
	b.WriteString("\n")
	if len(t.passed) > 0 {
		var names []string
		for _, c := range t.passed {
			names = append(names, c.Name)
		}
		b.WriteString("- Checks passed: " + codeList(names) + "\n")
	} else if t.run.CheckedSHA != "" {
		b.WriteString("- Checks: none expected\n")
	}
	b.WriteString("\nMove the ticket to Done once you've accepted it, or comment what's wrong and move it to Ready for a follow-up.\n")
	return b.String()
}
