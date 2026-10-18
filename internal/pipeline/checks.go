package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/tmpl"
)

// How often checks are polled, and how long Gosha waits after they all
// finish for late-starting ones (DESIGN §8.4). Variables for tests.
var (
	checkPoll   = 15 * time.Second
	checkSettle = 30 * time.Second
)

// checkResult is the outcome of waiting for checks on one head.
type checkResult struct {
	checks []github.Check
	// failed are completed checks that block a merge.
	failed []github.Check
	// missing are expected checks that never started within start_grace.
	missing []string
	// pending are checks still running at checks.timeout.
	pending []string
}

func (c checkResult) ok() bool { return len(c.failed)+len(c.missing)+len(c.pending) == 0 }

// checks waits for CI on the branch head and merges once it's green, or
// gives the failures to the implementer (DESIGN §8.4).
func (t *ticket) checks(ctx context.Context) error {
	if err := t.save(ctx, StepChecks); err != nil {
		return err
	}
	head, err := t.p.Repo.RevParse(ctx, "refs/heads/"+t.run.Branch)
	if err != nil {
		return err
	}
	res, err := t.waitChecks(ctx, head)
	if err != nil {
		return err
	}
	if res.ok() {
		t.run.CheckedSHA, t.run.ChecksResult = head, ""
		if err := t.save(ctx, StepChecks); err != nil {
			return err
		}
		t.passed = res.checks
		msg := fmt.Sprintf("%s · checks ✅ %d/%d", t.issue.Identifier, len(res.checks), len(res.checks))
		if len(res.checks) == 0 {
			msg = fmt.Sprintf("%s · no checks expected", t.issue.Identifier)
		}
		t.notify(msg, t.prLink())
		return t.merge(ctx)
	}
	if len(res.failed)+len(res.pending) == 0 {
		// Only checks that never started: nothing in the code to fix.
		return t.block(ctx, fmt.Sprintf("These checks never started on the pull request within `checks.start_grace` (%v): %s. "+
			"Check that `checks.required` names them exactly as GitHub does and that their workflows run on pull requests.",
			t.p.Config.Checks.StartGrace, codeList(res.missing)), nil)
	}
	names := failedNames(res)
	limit := t.p.Config.Checks.MaxRounds
	if t.run.CheckRound >= limit {
		return t.block(ctx, fmt.Sprintf("Checks still fail after %d fix rounds (`checks.max_rounds`): %s.\n\n%s",
			limit, codeList(names), checkLinks(res)), nil)
	}
	fb := tmpl.Feedback{CI: t.ciFeedback(ctx, res)}
	if err := t.recordFailure(ctx, head, fb); err != nil {
		return err
	}
	t.notify(fmt.Sprintf("%s · checks ❌ %s → fixing %d/%d", t.issue.Identifier, strings.Join(names, ", "), t.run.CheckRound, limit), t.prLink())
	return t.fix(ctx, fb)
}

// recordFailure starts a check round for a failing head and saves its
// feedback, so an interrupted fix pass continues with the same feedback
// instead of counting another round.
func (t *ticket) recordFailure(ctx context.Context, head string, fb tmpl.Feedback) error {
	t.run.CheckRound++
	path := filepath.Join(t.e.Home.Project(t.p.Name).Runs(), t.issue.Identifier, fmt.Sprintf("checks-%d.json", t.run.CheckRound))
	data, err := json.Marshal(fb)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	t.run.CheckedSHA, t.run.ChecksResult = head, path
	return t.save(ctx, StepFix)
}

// savedFailure reads back the feedback recorded for the current head, or
// nil if there's none.
func (t *ticket) savedFailure(ctx context.Context) (*tmpl.Feedback, error) {
	if t.run.ChecksResult == "" {
		return nil, nil
	}
	head, err := t.p.Repo.RevParse(ctx, "refs/heads/"+t.run.Branch)
	if err != nil || head != t.run.CheckedSHA {
		return nil, err
	}
	data, err := os.ReadFile(t.run.ChecksResult)
	if err != nil {
		return nil, nil
	}
	var fb tmpl.Feedback
	if json.Unmarshal(data, &fb) != nil {
		return nil, nil
	}
	return &fb, nil
}

// waitChecks polls head's checks until every expected one has started and
// all have finished, start_grace or checks.timeout runs out, or ctx ends.
func (t *ticket) waitChecks(ctx context.Context, head string) (checkResult, error) {
	cfg := t.p.Config.Checks
	required := cfg.Required
	anyExpected := false
	if len(required) == 0 {
		var err error
		if anyExpected, err = t.p.Repo.HasCIWorkflows(ctx, "refs/remotes/origin/"+t.run.BaseBranch); err != nil {
			return checkResult{}, err
		}
		if !anyExpected {
			return checkResult{}, nil
		}
	}
	start := time.Now()
	settled := false
	for {
		cs, err := t.prs.Checks(ctx, head)
		if err != nil {
			return checkResult{}, err
		}
		res := checkResult{checks: cs}
		for _, name := range required {
			if !slices.ContainsFunc(cs, func(c github.Check) bool { return c.Name == name }) {
				res.missing = append(res.missing, name)
			}
		}
		if anyExpected && len(cs) == 0 {
			res.missing = []string{"any check"}
		}
		for _, c := range cs {
			switch {
			case c.Status != "completed":
				res.pending = append(res.pending, c.Name)
			case !c.Passed():
				res.failed = append(res.failed, c)
			}
		}
		elapsed := time.Since(start)
		switch {
		case len(res.missing) == 0 && len(res.pending) == 0:
			if settled {
				return res, nil
			}
			// Wait once more for checks that start late.
			settled = true
			if err := sleep(ctx, checkSettle); err != nil {
				return checkResult{}, err
			}
			continue
		case len(res.missing) > 0 && elapsed >= cfg.StartGrace:
			res.pending = nil
			return res, nil
		case elapsed >= cfg.Timeout:
			return res, nil
		}
		settled = false
		if err := sleep(ctx, checkPoll); err != nil {
			return checkResult{}, err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-tm.C:
		return nil
	}
}

// ciFeedback is .Feedback.CI: each failing check with the failing step of
// its job log, and the checks that timed out.
func (t *ticket) ciFeedback(ctx context.Context, res checkResult) string {
	var b strings.Builder
	for _, c := range res.failed {
		fmt.Fprintf(&b, "## %s (%s)\n\n", c.Name, c.Conclusion)
		if c.Summary != "" {
			b.WriteString(c.Summary + "\n\n")
		}
		if c.JobID != 0 {
			log, err := t.prs.JobLog(ctx, c.JobID)
			if err != nil {
				t.e.Log.Warn("job log", "ticket", t.issue.Identifier, "check", c.Name, "err", err)
			} else if ex := failingStep(log); ex != "" {
				b.WriteString("```\n" + t.e.Redactor.Redact(ex) + "\n```\n\n")
			}
		}
		if c.URL != "" {
			b.WriteString(c.URL + "\n\n")
		}
	}
	for _, name := range res.pending {
		fmt.Fprintf(&b, "## %s (timed out)\n\nStill running after `checks.timeout` (%v).\n\n", name, t.p.Config.Checks.Timeout)
	}
	return strings.TrimSpace(b.String())
}

func failedNames(res checkResult) []string {
	var out []string
	for _, c := range res.failed {
		out = append(out, c.Name)
	}
	return append(out, res.pending...)
}

func checkLinks(res checkResult) string {
	var b strings.Builder
	for _, c := range res.failed {
		fmt.Fprintf(&b, "- `%s`: %s", c.Name, c.Conclusion)
		if c.URL != "" {
			b.WriteString(" · " + c.URL)
		}
		b.WriteString("\n")
	}
	for _, name := range res.pending {
		fmt.Fprintf(&b, "- `%s`: timed out\n", name)
	}
	return b.String()
}

func codeList(names []string) string {
	var q []string
	for _, n := range names {
		q = append(q, "`"+n+"`")
	}
	return strings.Join(q, ", ")
}

// fix runs the implementer on failed checks or merge conflicts, one check
// round, then sends its change back through review and checks (DESIGN
// §8.4, §8.5).
func (t *ticket) fix(ctx context.Context, fb tmpl.Feedback) error {
	if err := t.save(ctx, StepFix); err != nil {
		return err
	}
	if fb.Conflicts != "" {
		// A fresh checkout carries the latest base for the agent to merge.
		if err := os.RemoveAll(t.checkout()); err != nil {
			return err
		}
	}
	round, limit := t.run.CheckRound, t.p.Config.Checks.MaxRounds
	res, changed, err := t.feedbackPass(ctx, round, fb)
	if res == nil {
		return err
	}
	summary := strings.TrimSpace(t.e.Redactor.Redact(res.Summary))
	reply := fmt.Sprintf("**Check round %d: fixed.**\n\n", round)
	if fb.Conflicts != "" {
		merged, err := t.p.Repo.IsAncestor(ctx, "refs/remotes/origin/"+t.run.BaseBranch, "refs/heads/"+t.run.Branch)
		if err != nil {
			return err
		}
		if !merged {
			return t.block(ctx, fmt.Sprintf("`%s` conflicts with this branch and the implementer didn't merge it. Its summary:\n\n%s",
				t.run.BaseBranch, quote(summary)), nil)
		}
		reply = fmt.Sprintf("**Check round %d: merged `%s` and resolved the conflicts.**\n\n", round, t.run.BaseBranch)
	} else if !changed {
		return t.block(ctx, "The checks failed and the implementer committed no fix. Its summary:\n\n"+quote(summary), nil)
	}
	reply += summary + "\n"
	if v := strings.TrimSpace(res.Verification); v != "" {
		reply += "\n### Verification\n" + t.e.Redactor.Redact(v) + "\n"
	}
	if err := t.prs.CreateComment(ctx, t.run.PRNumber, reply); err != nil {
		return err
	}
	t.run.LastReply = res.Summary
	t.run.ReviewedSHA, t.run.ReviewID = "", 0
	t.run.CheckedSHA, t.run.ChecksResult = "", ""
	if err := t.save(ctx, StepReview); err != nil {
		return err
	}
	diff, err := t.p.Repo.Diff(ctx, t.run.BaseBranch, t.run.Branch)
	if err != nil {
		return err
	}
	t.notify(fmt.Sprintf("%s · fixed (check round %d/%d) · PR now %s", t.issue.Identifier, round, limit, diff), t.prLink())
	t.recheck = true
	return t.reviewLoop(ctx, false)
}
