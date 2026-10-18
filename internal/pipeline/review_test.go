package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/agent"
	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/linear"
)

// commitEach commits a new file on every implementer call.
func commitEach(t *testing.T) func(int, string) string {
	return func(call int, dir string) string { commitFile(t, dir, fmt.Sprintf("f%d.go", call)); return doneResult }
}

func TestReviewFixRound(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = commitEach(t)
	h.agent.review = func(call int, _ string) string {
		if call == 1 {
			return requestChanges
		}
		return approve
	}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepChecks || r.ReviewRound != 2 || r.LastReply != "Added login." || len(h.agent.calls) != 2 || len(h.prs.reviews) != 2 {
		t.Fatalf("run = %+v, %d impl calls, %d reviews", r, len(h.agent.calls), len(h.prs.reviews))
	}
	addr := h.agent.calls[1]
	if addr.Resume != "sess-1" || addr.Dir != h.agent.calls[0].Dir || !strings.Contains(addr.Transcript, "-address.jsonl") ||
		!strings.Contains(addr.Prompt, "**must** `login.go:1`: add a test") || !strings.Contains(addr.Prompt, "This is round 1.") {
		t.Errorf("address spec = %+v", addr)
	}
	head := gitIn(t, h.upstream, "rev-parse", r.Branch)
	if h.prs.reviews[1].sha != head || h.prs.reviews[0].sha == head {
		t.Error("the second review isn't on the pushed fix")
	}
	if !strings.Contains(h.prs.reviews[0].body, "❌ **Changes requested** · Gosha review 1/3") {
		t.Errorf("first review = %s", h.prs.reviews[0].body)
	}
	if len(h.prs.comments) != 1 || !strings.Contains(h.prs.comments[0], "**Round 1 addressed.**") {
		t.Errorf("PR comments = %v", h.prs.comments)
	}
	prev := h.agent.reviews[1].Prompt
	for _, want := range []string{"review round 2", "Round 1's review (request_changes): Missing tests.", "add a test", "> Added login."} {
		if !strings.Contains(prev, want) {
			t.Errorf("second review prompt missing %q", want)
		}
	}
	ev := strings.Join(h.events.take(), "\n")
	for _, want := range []string{"review 1/3 ❌ · 1 must", "addressed 1 comments (round 1) · PR now +2 −0 in 2 files", "review 2/3 ✅"} {
		if !strings.Contains(ev, want) {
			t.Errorf("events missing %q:\n%s", want, ev)
		}
	}
}

func TestReviewRoundsExhausted(t *testing.T) {
	h := newHarness(t, "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: x\nreview:\n  max_rounds: 2\n")
	h.agent.act = commitEach(t)
	h.agent.review = func(int, string) string { return requestChanges }
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepBlocked || r.ReviewRound != 2 || len(h.prs.reviews) != 2 || len(h.agent.calls) != 2 {
		t.Fatalf("run = %+v, %d reviews, %d impl calls", r, len(h.prs.reviews), len(h.agent.calls))
	}
	c := h.lin.comments[0]
	for _, want := range []string{"while reviewing the pull request", "after 2 review rounds", "**must** `login.go:1`: add a test"} {
		if !strings.Contains(c, want) {
			t.Errorf("Blocked comment missing %q:\n%s", want, c)
		}
	}
	if h.lin.state() != states.Blocked.ID {
		t.Errorf("state = %s", h.lin.state())
	}

	// Ready again with no comment: the limit resets and the PR is reviewed afresh.
	h.agent.review = func(int, string) string { return approve }
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if r := h.run(); r.Step != StepChecks || r.ReviewRound != 1 || len(h.agent.calls) != 2 {
		t.Errorf("after Ready: %+v, %d impl calls", r, len(h.agent.calls))
	}
}

func TestHumanPRCommentForcesAddress(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = commitEach(t)
	h.prs.feedback = []github.Feedback{{Author: "ann", Kind: github.FeedbackInline, Path: "f1.go", Line: 1, Body: "please rename", At: time.Now()}}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepChecks || len(h.agent.calls) != 2 || len(h.prs.reviews) != 2 || !r.PRCursor.Equal(h.prs.feedback[0].At) {
		t.Fatalf("run = %+v, %d impl calls, %d reviews", r, len(h.agent.calls), len(h.prs.reviews))
	}
	p := h.agent.calls[1].Prompt
	for _, want := range []string{"From people on the pull request", "**ann** (inline comment on `f1.go:1`): please rename"} {
		if !strings.Contains(p, want) {
			t.Errorf("address prompt missing %q:\n%s", want, p)
		}
	}
}

func TestReadyOnOpenPRWithCommentAddressesFirst(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = commitEach(t)
	h.pickup()
	note := linear.Comment{ID: "c1", Body: "Also handle logout.", Author: "Ann", Human: true, CreatedAt: time.Now()}
	if err := h.pickup(note); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepChecks || r.ReviewRound != 1 || len(h.agent.calls) != 2 || len(h.prs.reviews) != 2 {
		t.Fatalf("run = %+v, %d impl calls, %d reviews", r, len(h.agent.calls), len(h.prs.reviews))
	}
	addr := h.agent.calls[1]
	if !strings.Contains(addr.Prompt, "Also handle logout.") || addr.Resume != "sess-1" || !strings.Contains(addr.Transcript, "address") {
		t.Errorf("address spec = %+v", addr)
	}
	if cur, _ := h.env.Store.CommentCursor(h.ctx, "demo", "NXT-12"); cur == nil || cur.CommentID != "c1" {
		t.Errorf("cursor = %+v", cur)
	}
}

func TestInterruptedAddressDoesntReviewTwice(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(call int, dir string) string {
		if call == 2 {
			return "" // usage limit during the address pass
		}
		commitFile(t, dir, fmt.Sprintf("f%d.go", call))
		return doneResult
	}
	h.agent.review = func(call int, _ string) string {
		if call == 1 {
			return requestChanges
		}
		return approve
	}
	var le *agent.LimitError
	if err := h.pickup(); !errors.As(err, &le) {
		t.Fatalf("err = %v", err)
	}
	if r := h.run(); r.Step != StepAddress || r.ReviewID != 1 || h.lin.state() != states.InProgress.ID {
		t.Fatalf("after limit: %+v", r)
	}
	// The run continues (the ticket is still In Progress): address with the
	// posted review, then review the fix. The first review isn't redone.
	h.issue.State = states.InProgress
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepChecks || len(h.agent.reviews) != 2 || len(h.prs.reviews) != 2 || r.ReviewRound != 2 {
		t.Fatalf("run = %+v, %d reviewer calls, %d posted", r, len(h.agent.reviews), len(h.prs.reviews))
	}
	if !strings.Contains(h.agent.calls[2].Prompt, "add a test") {
		t.Error("the continued address pass lost the review")
	}
}

func TestInterruptedAfterPostingReview(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = commitEach(t)
	h.pickup()
	// Stopped after the review was posted but before the verdict was acted on.
	r := h.run()
	r.Step = StepReview
	h.env.Store.UpdateRun(h.ctx, r)
	h.issue.State = states.InProgress
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if r := h.run(); r.Step != StepChecks || len(h.agent.reviews) != 1 || len(h.prs.reviews) != 1 || r.ReviewRound != 1 {
		t.Errorf("run = %+v, %d reviewer calls, %d posted", r, len(h.agent.reviews), len(h.prs.reviews))
	}
}

func TestAddressWithoutChanges(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(call int, dir string) string {
		if call == 1 {
			commitFile(t, dir, "login.go")
		}
		return `{"status":"done","summary":"The test exists already in login_test.go."}`
	}
	h.agent.review = func(call int, _ string) string {
		if call == 1 {
			return requestChanges
		}
		return approve
	}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if r := h.run(); r.Step != StepChecks || len(h.prs.reviews) != 2 || h.prs.reviews[0].sha != h.prs.reviews[1].sha {
		t.Errorf("run = %+v, reviews %+v", r, h.prs.reviews)
	}
	if len(h.prs.comments) != 1 || !strings.Contains(h.prs.comments[0], "no code changes") || !strings.Contains(h.prs.comments[0], "exists already") {
		t.Errorf("PR comments = %v", h.prs.comments)
	}
}

func TestReviewerUsageLimit(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = commitEach(t)
	h.agent.review = func(int, string) string { return "" }
	var le *agent.LimitError
	if err := h.pickup(); !errors.As(err, &le) {
		t.Fatalf("err = %v", err)
	}
	if r := h.run(); r.Step != StepReview || h.lin.state() != states.InProgress.ID || len(h.prs.reviews) != 0 {
		t.Errorf("run = %+v", r)
	}
}

func TestCapDiff(t *testing.T) {
	d := strings.Repeat("+line\n", maxDiff/6+100)
	got := capDiff(d)
	if len(got) > maxDiff+100 || !strings.Contains(got, "diff cut at 100 KB") || !strings.HasSuffix(strings.Split(got, "…")[0], "\n") {
		t.Errorf("capDiff: %d bytes, tail %q", len(got), got[len(got)-60:])
	}
	if capDiff("+x\n") != "+x\n" {
		t.Error("short diff changed")
	}
}
