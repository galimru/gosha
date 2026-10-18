package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// stepNames are the steps in plain words, for the Blocked comment.
var stepNames = map[string]string{
	StepClaimed:   "starting the ticket",
	StepImplement: "implementing the ticket",
	StepPush:      "pushing the branch",
	StepPR:        "opening the pull request",
	StepReview:    "reviewing the pull request",
	StepAddress:   "addressing feedback on the pull request",
	StepChecks:    "waiting for checks",
	StepFix:       "fixing failed checks",
	StepSync:      "merging the base branch into the pull request",
	StepMerge:     "merging the pull request",
	StepDeploy:    "watching the deploy",
}

// needAnswer is what a Blocked comment asks for unless the step says
// otherwise.
const needAnswer = "answer in a comment, then move the ticket back to Ready and I'll continue where I stopped. " +
	"To start over from scratch, close the pull request instead."

// block posts the Blocked comment, moves the ticket to Blocked and keeps
// the branch, PR and checkout so a later Ready resumes.
func (t *ticket) block(ctx context.Context, reason string, questions []string) error {
	return t.blockWith(ctx, reason, questions, needAnswer)
}

// blockWith is block with its own "What I need" (lowercase, a sentence).
func (t *ticket) blockWith(ctx context.Context, reason string, questions []string, need string) error {
	reason = t.e.Redactor.Redact(strings.TrimSpace(reason))
	step := t.run.Step
	if step == StepBlocked {
		step = StepClaimed
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Gosha is blocked** while %s.\n\n%s\n", stepNames[step], reason)
	if len(questions) > 0 {
		b.WriteString("\n**Questions**\n")
		for _, q := range questions {
			b.WriteString("- " + t.e.Redactor.Redact(q) + "\n")
		}
	}
	b.WriteString("\n**What I need:** " + need + "\n")
	var links []string
	if t.run.PRURL != "" {
		links = append(links, fmt.Sprintf("PR: %s", t.run.PRURL))
	}
	if t.run.Branch != "" {
		links = append(links, fmt.Sprintf("Branch: `%s`", t.run.Branch))
	}
	if len(links) > 0 {
		b.WriteString("\n" + strings.Join(links, " · ") + "\n")
	}
	if _, err := t.issues.CreateComment(ctx, t.issue.ID, b.String()); err != nil {
		return err
	}
	t.leave()
	if err := t.issues.Transition(ctx, t.issue.ID, t.states.Blocked.ID); err != nil {
		return err
	}
	t.run.BlockedReason, t.run.BlockedAt = reason, time.Now()
	if err := t.save(ctx, StepBlocked); err != nil {
		return err
	}
	t.e.Log.Warn("blocked", "project", t.p.Name, "ticket", t.issue.Identifier, "step", step, "reason", reason)
	short := reason
	if i := strings.IndexByte(short, '\n'); i > 0 {
		short = short[:i]
	}
	t.notify(fmt.Sprintf("%s · ⛔ blocked while %s: %s", t.issue.Identifier, stepNames[step], short), t.ticketLink(), t.prLink())
	return nil
}
