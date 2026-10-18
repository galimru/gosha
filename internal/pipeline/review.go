package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/galimru/gosha/internal/agent"
	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/tmpl"
)

// roleAddress names the implementer's feedback passes (transcripts
// <n>-address.jsonl); they get the implementer's permissions.
const roleAddress = "address"

// resumeOpen continues a run whose PR is open (DESIGN §8.1, §8.3). From
// Ready, the round limit resets; new ticket comments go to the
// implementer first, otherwise the PR gets a fresh review. An
// interrupted run continues at the step it stopped at.
func (t *ticket) resumeOpen(ctx context.Context) error {
	if !t.fromReady {
		switch t.run.Step {
		case StepChecks, StepSync, StepMerge:
			return t.checks(ctx)
		case StepFix:
			fb, err := t.savedFailure(ctx)
			if err != nil {
				return err
			}
			if fb == nil {
				return t.checks(ctx)
			}
			return t.fix(ctx, *fb)
		}
		return t.reviewLoop(ctx, t.run.Step == StepAddress)
	}
	t.run.ReviewRound, t.run.CheckRound = 0, 0
	t.run.CheckedSHA, t.run.ChecksResult = "", ""
	if len(t.comments) > 0 {
		return t.reviewLoop(ctx, true)
	}
	t.run.ReviewedSHA, t.run.ReviewID = "", 0
	return t.reviewLoop(ctx, false)
}

// reviewLoop reviews and addresses until the reviewer approves (then on to
// checks), the round limit is hit or the ticket goes Blocked (DESIGN §8.3).
// With addressFirst, it starts with an address pass.
func (t *ticket) reviewLoop(ctx context.Context, addressFirst bool) error {
	if addressFirst {
		if err := t.address(ctx); err != nil || t.run.Step == StepBlocked {
			return err
		}
	}
	limit := t.p.Config.Review.MaxRounds
	for {
		before, recheck := t.run.ReviewRound, t.recheck
		res, err := t.review(ctx)
		t.recheck = false
		if err != nil || res == nil {
			return err
		}
		if res.Verdict == agent.VerdictApprove && res.Count(agent.SeverityMust) == 0 {
			if recheck {
				t.run.ReviewRound = before
			}
			t.run.BlockedReason = ""
			return t.checks(ctx)
		}
		if t.run.ReviewRound >= limit {
			return t.block(ctx, roundsExhausted(limit, res), nil)
		}
		if err := t.address(ctx); err != nil || t.run.Step == StepBlocked {
			return err
		}
	}
}

// review runs one review round on the branch head and posts it as a PR
// review. It returns a nil result when the step ended (Blocked, usage
// limit, shutdown).
//
// A review already run or posted for the same head is reused, so a run
// that stopped mid-round doesn't review or post twice.
func (t *ticket) review(ctx context.Context) (*agent.ReviewerResult, error) {
	if err := t.save(ctx, StepReview); err != nil {
		return nil, err
	}
	head, err := t.p.Repo.RevParse(ctx, "refs/heads/"+t.run.Branch)
	if err != nil {
		return nil, err
	}
	var res *agent.ReviewerResult
	if t.run.ReviewedSHA == head && t.run.ReviewResult != "" {
		if res, err = agent.ReadReview(t.run.ReviewResult); err != nil {
			res = nil
		}
	}
	if res == nil {
		t.run.ReviewID = 0
		if res, err = t.runReviewer(ctx, head); res == nil {
			return nil, err
		}
	}
	limit := t.p.Config.Review.MaxRounds
	if t.run.ReviewID == 0 {
		round := t.run.ReviewRound + 1
		id, err := t.prs.CreateReview(ctx, t.run.PRNumber, head, t.reviewBody(res, t.roundLabel(round, limit)), t.reviewComments(res))
		if err != nil {
			return nil, err
		}
		t.run.ReviewID, t.run.ReviewRound = id, round
		if err := t.save(ctx, StepReview); err != nil {
			return nil, err
		}
	}
	t.e.Log.Info("reviewed", "project", t.p.Name, "ticket", t.issue.Identifier, "round", t.run.ReviewRound,
		"verdict", res.Verdict, "must", res.Count(agent.SeverityMust))
	return res, nil
}

// runReviewer runs the reviewer in a fresh clone of the branch head. It
// reads the change there with git diff; in the sandbox it can't write the
// clone (DESIGN §7).
func (t *ticket) runReviewer(ctx context.Context, head string) (*agent.ReviewerResult, error) {
	dir := t.workspace().Review()
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	_, rp, err := t.prepare(ctx, dir, agent.RoleReviewer)
	if err != nil || t.run.Step == StepBlocked {
		return nil, err
	}
	resultFile := filepath.Join(rp.agent.RunDir, "review.json")
	prompt, err := agent.RenderPrompt(ctx, t.p.Repo, t.p.Commit, "review.md", tmpl.Data{
		Project: t.p.Name, Ticket: Ticket(t.issue, nil), Context: t.kb,
		Branch: t.run.Branch, BaseBranch: t.run.BaseBranch, ResultFile: resultFile,
		Round: t.run.ReviewRound + 1, Feedback: tmpl.Feedback{Previous: t.previousRound()},
	})
	if err != nil {
		return nil, t.block(ctx, err.Error(), nil)
	}
	spec := t.spec(prompt, dir, rp)
	spec.DisallowedTools = agent.ReviewerDisallowed
	end, err := t.agentPass(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	res, _, err := t.e.review(t.actx, spec, resultFile)
	if err != nil {
		return nil, t.agentFailed(ctx, "reviewer", err)
	}
	t.run.ReviewedSHA, t.run.ReviewResult = head, resultFile
	return res, t.save(ctx, StepReview)
}

// previousRound is what the next reviewer is told about the last round:
// its findings and the implementer's reply.
func (t *ticket) previousRound() string {
	if t.run.ReviewRound == 0 || t.run.ReviewResult == "" {
		return ""
	}
	prev, err := agent.ReadReview(t.run.ReviewResult)
	if err != nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Round %d's review (%s): %s\n", t.run.ReviewRound, prev.Verdict, strings.TrimSpace(prev.Summary))
	writeComments(&b, prev.Comments)
	if t.run.LastReply != "" {
		b.WriteString("\nThe implementer's reply:\n\n" + quote(t.run.LastReply) + "\n")
	}
	return b.String()
}

// address runs the implementer on the feedback: the last review (if it was
// for the current head) and new human ticket comments. It pushes what changed, replies on the PR and leaves the
// run ready for the next review round.
func (t *ticket) address(ctx context.Context) error {
	if err := t.save(ctx, StepAddress); err != nil {
		return err
	}
	head, err := t.p.Repo.RevParse(ctx, "refs/heads/"+t.run.Branch)
	if err != nil {
		return err
	}
	var rev *agent.ReviewerResult
	if t.run.ReviewedSHA == head && t.run.ReviewResult != "" {
		rev, _ = agent.ReadReview(t.run.ReviewResult)
	}
	round := max(t.run.ReviewRound, 1)
	res, changed, err := t.feedbackPass(ctx, round, tmpl.Feedback{Review: renderFeedback(rev)})
	if res == nil {
		return err
	}
	reply := fmt.Sprintf("**Round %d addressed.**\n\n", round)
	if !changed {
		reply = fmt.Sprintf("**Round %d: no code changes.**\n\n", round)
	}
	reply += strings.TrimSpace(t.e.Redactor.Redact(res.Summary)) + "\n"
	if v := strings.TrimSpace(res.Verification); v != "" {
		reply += "\n### Verification\n" + t.e.Redactor.Redact(v) + "\n"
	}
	if err := t.prs.CreateComment(ctx, t.run.PRNumber, reply); err != nil {
		return err
	}
	t.run.LastReply = res.Summary
	t.run.ReviewedSHA, t.run.ReviewID = "", 0
	if err := t.save(ctx, StepReview); err != nil {
		return err
	}
	t.e.Log.Info("addressed", "project", t.p.Name, "ticket", t.issue.Identifier, "round", round, "changed", changed)
	return nil
}

// feedbackPass runs the implementer with address.md in the ticket checkout,
// resuming its session, then collects and pushes what it committed. It
// returns a nil result when the step ended (Blocked, usage limit, shutdown).
func (t *ticket) feedbackPass(ctx context.Context, round int, fb tmpl.Feedback) (*agent.ImplementerResult, bool, error) {
	head, err := t.p.Repo.RevParse(ctx, "refs/heads/"+t.run.Branch)
	if err != nil {
		return nil, false, err
	}
	checkout := t.checkout()
	fresh, rp, err := t.prepare(ctx, checkout, roleAddress)
	if err != nil || t.run.Step == StepBlocked {
		return nil, false, err
	}
	if err := t.newComments(ctx); err != nil {
		return nil, false, err
	}
	resultFile := filepath.Join(rp.agent.RunDir, "result.json")
	prompt, err := agent.RenderPrompt(ctx, t.p.Repo, t.p.Commit, "address.md", tmpl.Data{
		Project: t.p.Name, Ticket: Ticket(t.issue, t.comments), Context: t.kb,
		Branch: t.run.Branch, BaseBranch: t.run.BaseBranch, ResultFile: resultFile,
		Round: round, Feedback: fb,
	})
	if err != nil {
		return nil, false, t.block(ctx, err.Error(), nil)
	}
	spec := t.spec(prompt, checkout, rp)
	if !fresh {
		spec.Resume = t.run.ImplSession
	}
	res, err := t.runImplementer(ctx, spec, resultFile)
	if res == nil {
		return nil, false, err
	}
	if ok, err := t.collect(ctx, checkout); !ok {
		return nil, false, err
	}
	newHead, err := t.p.Repo.RevParse(ctx, "refs/heads/"+t.run.Branch)
	if err != nil {
		return nil, false, err
	}
	changed := newHead != head
	if changed {
		if ok, err := t.push(ctx); !ok {
			return nil, false, err
		}
	}
	return res, changed, nil
}

// reviewBody is the PR review's body: the verdict mark (an App can't
// approve its own PR, DESIGN §8.6), the round and the summary.
func (t *ticket) reviewBody(res *agent.ReviewerResult, label string) string {
	verdict := "Changes requested"
	if res.Verdict == agent.VerdictApprove {
		verdict = "Approved"
	}
	return fmt.Sprintf("%s **%s** · %s\n\n%s\n\n%d must · %d should · %d nit",
		verdictMark(res), verdict, label, strings.TrimSpace(t.e.Redactor.Redact(res.Summary)),
		res.Count(agent.SeverityMust), res.Count(agent.SeverityShould), res.Count(agent.SeverityNit))
}

func (t *ticket) reviewComments(res *agent.ReviewerResult) []github.ReviewComment {
	var out []github.ReviewComment
	for _, c := range res.Comments {
		out = append(out, github.ReviewComment{Path: c.Path, Line: c.Line,
			Body: fmt.Sprintf("**%s:** %s", c.Severity, t.e.Redactor.Redact(c.Body))})
	}
	return out
}

// roundLabel names a review round, e.g. "Gosha review 2/3". A review of a
// fix for failed checks or conflicts is a re-review: it only takes a round
// if it requests changes.
func (t *ticket) roundLabel(round, limit int) string {
	if t.recheck {
		return "Gosha re-review after fixes"
	}
	return fmt.Sprintf("Gosha review %d/%d", round, limit)
}

func verdictMark(res *agent.ReviewerResult) string {
	if res.Verdict == agent.VerdictApprove && res.Count(agent.SeverityMust) == 0 {
		return "✅"
	}
	return "❌"
}

// renderFeedback is .Feedback.Review for the address prompt.
func renderFeedback(rev *agent.ReviewerResult) string {
	if rev == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## From the reviewer (%s)\n\n%s\n", rev.Verdict, strings.TrimSpace(rev.Summary))
	writeComments(&b, rev.Comments)
	return strings.TrimRight(b.String(), "\n")
}

func writeComments(b *strings.Builder, cs []agent.ReviewComment) {
	if len(cs) == 0 {
		return
	}
	b.WriteString("\n")
	for _, c := range cs {
		fmt.Fprintf(b, "- **%s** %s: %s\n", c.Severity, location(c.Path, c.Line), indent(c.Body))
	}
}

// roundsExhausted is the Blocked reason when the review limit is hit.
func roundsExhausted(limit int, res *agent.ReviewerResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The pull request still isn't approved after %d review rounds (`review.max_rounds`).\n", limit)
	var must []agent.ReviewComment
	for _, c := range res.Comments {
		if c.Severity == agent.SeverityMust {
			must = append(must, c)
		}
	}
	if len(must) > 0 {
		b.WriteString("\n**Unresolved reviewer findings**\n")
		writeComments(&b, must)
	} else if res.Verdict != agent.VerdictApprove {
		b.WriteString("\nThe reviewer's last verdict: " + strings.TrimSpace(res.Summary) + "\n")
	}
	return b.String()
}

func location(path string, line int) string {
	if line > 0 {
		return fmt.Sprintf("`%s:%d`", path, line)
	}
	return "`" + path + "`"
}

func indent(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ") }

func quote(s string) string { return "> " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ") }
