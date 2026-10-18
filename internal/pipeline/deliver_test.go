package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/linear"
)

const deployConfig = "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\n" +
	"deploy:\n  workflow: deploy.yml\n  url: https://demo.example.com\n  timeout: 50ms\n"

func deployed(conclusion string) func(int, string) *github.WorkflowRun {
	return func(call int, sha string) *github.WorkflowRun {
		if call == 1 {
			return &github.WorkflowRun{ID: 5, Status: "in_progress", URL: "https://run/5"}
		}
		return &github.WorkflowRun{ID: 5, Status: "completed", Conclusion: conclusion, URL: "https://run/5"}
	}
}

func deployHarness(t *testing.T) *harness {
	h := newHarness(t, deployConfig)
	deployPoll = time.Millisecond
	h.agent.act = commitEach(t)
	h.prs.checks = green
	return h
}

func TestDeployWatched(t *testing.T) {
	h := deployHarness(t)
	h.prs.deploys = deployed("success")
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.lastRun()
	if r.Outcome != OutcomeDelivered || r.Step != StepDeploy || h.prs.deployCalls != 2 || h.lin.state() != states.InReview.ID {
		t.Fatalf("run = %+v, %d deploy calls, linear %+v", r, h.prs.deployCalls, h.lin)
	}
	if h.onUpstream(r.Branch) {
		t.Error("branch not deleted")
	}
	for _, want := range []string{"**Deploy:** ✅ `deploy.yml` success · [run](https://run/5) · https://demo.example.com"} {
		if !strings.Contains(h.lin.comments[0], want) {
			t.Errorf("Delivered comment missing %q:\n%s", want, h.lin.comments[0])
		}
	}
	if ev := strings.Join(h.events.take(), "\n"); !strings.Contains(ev, "deploy ✅ success") {
		t.Errorf("events:\n%s", ev)
	}
}

func TestDeployFailureDeliversThenFollowsUp(t *testing.T) {
	h := deployHarness(t)
	h.prs.deploys = deployed("failure")
	h.prs.jobs = []github.Job{{ID: 1, Name: "build", Conclusion: "success"}, {ID: 9, Name: "ship", Conclusion: "failure"}}
	h.prs.logs = map[int64]string{9: "##[group]Run ./ship.sh\nshipping\n##[error]missing DEPLOY_ENV\n##[group]Post job cleanup\n"}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	first := h.lastRun()
	if first.Outcome != OutcomeDelivered || h.lin.state() != states.InReview.ID {
		t.Fatalf("run = %+v, linear %+v", first, h.lin)
	}
	c := h.lin.comments[0]
	for _, want := range []string{"**Deploy:** ❌ `deploy.yml` failure · [run](https://run/5)", "**ship** (failure)", "missing DEPLOY_ENV", "for a follow-up"} {
		if !strings.Contains(c, want) {
			t.Errorf("Delivered comment missing %q:\n%s", want, c)
		}
	}
	if strings.Contains(c, "**build**") || strings.Contains(c, "https://demo.example.com") {
		t.Errorf("Delivered comment lists a passing job or the deploy URL:\n%s", c)
	}

	// A human asks for a fix: a follow-up on the merged code.
	h.prs.deploys = deployed("success")
	if err := h.pickup(linear.Comment{ID: "c1", Body: "Set DEPLOY_ENV in the script.", Human: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r := h.lastRun()
	if r.ID == first.ID || r.Mode != ModeFollowUp || r.Outcome != OutcomeDelivered || r.Branch != "gosha/NXT-12-add-login-2" {
		t.Fatalf("follow-up run = %+v", r)
	}
	p := h.agent.calls[1].Prompt
	for _, want := range []string{"# This is a follow-up", "[PR #1](https://github.com/o/r/pull/1)", "Its deploy: failure", "missing DEPLOY_ENV", "Set DEPLOY_ENV in the script."} {
		if !strings.Contains(p, want) {
			t.Errorf("follow-up prompt missing %q", want)
		}
	}
	if h.prs.titles[1] != "NXT-12: Add login (follow-up)" || !strings.HasPrefix(h.prs.bodies[1], "Follow-up to #1.") {
		t.Errorf("follow-up PR = %q\n%s", h.prs.titles[1], h.prs.bodies[1])
	}
}

func TestNoDeployRunDelivers(t *testing.T) {
	h := deployHarness(t)
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if r := h.lastRun(); r.Outcome != OutcomeDelivered || !strings.Contains(h.lin.comments[0], "❌ `deploy.yml` no run") ||
		!strings.Contains(h.lin.comments[0], "No run of `deploy.yml` started for the merge commit") {
		t.Errorf("run = %+v, comment:\n%s", r, h.lin.comments[0])
	}
}

func TestInterruptedDeployWatchResumes(t *testing.T) {
	h := deployHarness(t)
	// Gosha stops while watching: the ticket stays In Progress.
	h.prs.deploys = func(int, string) *github.WorkflowRun {
		h.prs.stopped = true
		h.prs.stop()
		return nil
	}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepDeploy || r.MergeSHA == "" {
		t.Fatalf("run = %+v", r)
	}
	h.prs.deploys, h.prs.deployCalls = deployed("success"), 0
	h.issue.State = states.InProgress
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if last := h.lastRun(); last.ID != r.ID || last.Outcome != OutcomeDelivered || !strings.Contains(h.lin.comments[0], "✅ `deploy.yml` success") {
		t.Errorf("run = %+v, comments %v", last, h.lin.comments)
	}
}

func TestFollowUpAfterDelivery(t *testing.T) {
	h := newHarness(t, "")
	h.prs.checks = green
	h.agent.act = func(call int, dir string) string {
		commitFile(t, dir, "login.go")
		if call == 1 {
			return `{"status":"done","summary":"Added login.","verification":"go test","left_out":["password reset"]}`
		}
		return doneResult
	}
	h.pickup()
	first := h.lastRun()
	if first.Outcome != OutcomeDelivered || !strings.Contains(h.lin.comments[0], "**Left out**\n- password reset") ||
		!strings.Contains(h.prs.bodies[0], "### Left out\n- password reset") {
		t.Fatalf("first run = %+v\n%s", first, h.lin.comments[0])
	}
	if err := h.pickup(linear.Comment{ID: "c1", Body: "The button is misaligned.", Author: "Ann", Human: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r := h.lastRun()
	if r.ID == first.ID || r.Mode != ModeFollowUp || r.Branch != "gosha/NXT-12-add-login-2" || r.Outcome != OutcomeDelivered {
		t.Fatalf("follow-up run = %+v", r)
	}
	p := h.agent.calls[1].Prompt
	for _, want := range []string{"# This is a follow-up", "merged into `main` as `" + first.MergeSHA + "`", "What the earlier change did:\n\nAdded login.", "The button is misaligned."} {
		if !strings.Contains(p, want) {
			t.Errorf("follow-up prompt missing %q:\n%s", want, p)
		}
	}
	if ev := strings.Join(h.events.take(), "\n"); !strings.Contains(ev, "picked up (fresh (follow-up))") {
		t.Errorf("events:\n%s", ev)
	}
}

func TestEmptyFollowUpBlocks(t *testing.T) {
	h := newHarness(t, "")
	h.prs.checks = green
	h.agent.act = commitEach(t)
	h.pickup()
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Mode != ModeFollowUp || r.Step != StepBlocked || len(h.agent.calls) != 1 || h.lin.state() != states.Blocked.ID {
		t.Fatalf("run = %+v, %d agent calls", r, len(h.agent.calls))
	}
	if c := h.lin.comments[1]; !strings.Contains(c, "already merged") || !strings.Contains(c, "comment what's wrong or what to change") {
		t.Errorf("Blocked comment = %s", c)
	}
	// The answer resumes the same run, still as a follow-up.
	if err := h.pickup(linear.Comment{ID: "c1", Body: "Add a logout link.", Human: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	last := h.lastRun()
	if last.ID != r.ID || last.Mode != ModeFollowUp || last.Outcome != OutcomeDelivered || !strings.Contains(h.agent.calls[1].Prompt, "# This is a follow-up") {
		t.Errorf("run = %+v", last)
	}
}

func TestCommentDuringPickupReachesAddress(t *testing.T) {
	h := newHarness(t, "")
	late := linear.Comment{ID: "c2", Body: "Also handle expired sessions.", Author: "Ann", Human: true, CreatedAt: time.Now().Add(time.Minute)}
	h.agent.act = func(call int, dir string) string {
		if call == 1 {
			// Written while the implementer works.
			h.lin.ticket = append(h.lin.ticket, late)
		}
		return commitEach(t)(call, dir)
	}
	h.agent.review = func(call int, _ string) string {
		if call == 1 {
			return requestChanges
		}
		return approve
	}
	early := linear.Comment{ID: "c1", Body: "Use OAuth.", Human: true, CreatedAt: time.Now()}
	if err := h.pickup(early); err != nil {
		t.Fatal(err)
	}
	p := h.agent.calls[1].Prompt
	if !strings.Contains(p, "Also handle expired sessions.") || !strings.Contains(p, "Use OAuth.") {
		t.Errorf("address prompt lacks the comments:\n%s", p)
	}
	if cur, _ := h.env.Store.CommentCursor(h.ctx, "demo", "NXT-12"); cur == nil || cur.CommentID != "c2" {
		t.Errorf("cursor = %+v", cur)
	}
}
