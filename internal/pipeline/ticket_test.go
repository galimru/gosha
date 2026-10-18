package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/agent"
	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/gitx"
	"github.com/galimru/gosha/internal/linear"
	"github.com/galimru/gosha/internal/store"
	"github.com/galimru/gosha/internal/tmpl"
)

type fakeLinear struct {
	transitions []string
	comments    []string
	attachments []string
	// ticket is the ticket's comments as Linear has them; pickup sets it,
	// and a test can add to it while Gosha works.
	ticket []linear.Comment
}

func (f *fakeLinear) Comments(context.Context, string) ([]linear.Comment, error) {
	return f.ticket, nil
}

func (f *fakeLinear) Transition(_ context.Context, _, stateID string) error {
	f.transitions = append(f.transitions, stateID)
	return nil
}

func (f *fakeLinear) CreateComment(_ context.Context, _, body string) (string, error) {
	f.comments = append(f.comments, body)
	return fmt.Sprint(len(f.comments)), nil
}

func (f *fakeLinear) CreateAttachment(_ context.Context, _, link, _, _ string) error {
	f.attachments = append(f.attachments, link)
	return nil
}

func (f *fakeLinear) state() string {
	if len(f.transitions) == 0 {
		return ""
	}
	return f.transitions[len(f.transitions)-1]
}

type fakePRs struct {
	prs      map[int]*github.PR
	closed   []int
	reviews  []postedReview
	comments []string
	// feedback is human PR feedback; HumanFeedback returns what's after since.
	feedback []github.Feedback
	// checks returns the checks on a head for each call. Without it, Gosha
	// is stopped while it waits for checks, as by a shutdown, so the run
	// stays open at StepChecks.
	checks     func(call int, sha string) []github.Check
	checkCalls int
	stop       context.CancelFunc
	stopped    bool
	logs       map[int64]string
	merged     []mergeCall
	mergeErr   error
	// deploys returns the deploy run for each call (none if nil), and jobs
	// a run's jobs.
	deploys     func(call int, sha string) *github.WorkflowRun
	deployCalls int
	jobs        []github.Job
	// titles and bodies of the PRs opened.
	titles, bodies []string
}

func (f *fakePRs) DeployRun(_ context.Context, _, sha, _ string) (*github.WorkflowRun, error) {
	f.deployCalls++
	if f.deploys == nil {
		return nil, nil
	}
	return f.deploys(f.deployCalls, sha), nil
}

func (f *fakePRs) RunJobs(context.Context, int64) ([]github.Job, error) { return f.jobs, nil }

type mergeCall struct {
	number             int
	sha, method, title string
}

func (f *fakePRs) Checks(_ context.Context, sha string) ([]github.Check, error) {
	f.checkCalls++
	if f.checks == nil {
		f.stopped = true
		f.stop()
		return nil, context.Canceled
	}
	return f.checks(f.checkCalls, sha), nil
}

func (f *fakePRs) JobLog(_ context.Context, id int64) (string, error) {
	if l, ok := f.logs[id]; ok {
		return l, nil
	}
	return "", errors.New("no log")
}

func (f *fakePRs) Merge(_ context.Context, n int, sha, method, title, _ string) (string, error) {
	if f.mergeErr != nil {
		err := f.mergeErr
		f.mergeErr = nil
		return "", err
	}
	f.merged = append(f.merged, mergeCall{n, sha, method, title})
	f.prs[n].State, f.prs[n].Merged = "closed", true
	return "merge-" + sha[:7], nil
}

type postedReview struct {
	sha, body string
	comments  []github.ReviewComment
}

func (f *fakePRs) CreateReview(_ context.Context, _ int, sha, body string, cs []github.ReviewComment) (int64, error) {
	f.reviews = append(f.reviews, postedReview{sha, body, cs})
	return int64(len(f.reviews)), nil
}

func (f *fakePRs) CreateComment(_ context.Context, _ int, body string) error {
	f.comments = append(f.comments, body)
	return nil
}

func (f *fakePRs) HumanFeedback(_ context.Context, _ int, since time.Time) ([]github.Feedback, error) {
	var out []github.Feedback
	for _, fb := range f.feedback {
		if fb.At.After(since) {
			out = append(out, fb)
		}
	}
	return out, nil
}

func (f *fakePRs) Token(context.Context) (string, error) { return "", nil }

func (f *fakePRs) PR(_ context.Context, n int) (*github.PR, error) {
	if p, ok := f.prs[n]; ok {
		return p, nil
	}
	return nil, errors.New("no such PR")
}

func (f *fakePRs) CreatePR(_ context.Context, branch, base, title, body string) (*github.PR, error) {
	f.titles, f.bodies = append(f.titles, title), append(f.bodies, body)
	n := len(f.prs) + 1
	p := &github.PR{Number: n, URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n), State: "open"}
	f.prs[n] = p
	return p, nil
}

func (f *fakePRs) ClosePR(_ context.Context, n int) error {
	f.prs[n].State = "closed"
	f.closed = append(f.closed, n)
	return nil
}

// fakeAgent stands in for the implementer and the reviewer.
type fakeAgent struct {
	t     *testing.T
	calls []agent.Spec
	// act runs in the checkout for each call and returns the result JSON.
	act func(call int, dir string) string
	// reviews are the reviewer's calls; review returns the result JSON for
	// each (approve if nil).
	reviews []agent.Spec
	review  func(call int, dir string) string
}

func (a *fakeAgent) reviewer(_ context.Context, s agent.Spec, _ string) (*agent.ReviewerResult, *agent.Outcome, error) {
	a.reviews = append(a.reviews, s)
	out := &agent.Outcome{SessionID: fmt.Sprintf("review-%d", len(a.reviews))}
	res := approve
	if a.review != nil {
		res = a.review(len(a.reviews), s.Dir)
	}
	if res == "" {
		return nil, out, &agent.LimitError{Status: "rejected", ResetsAt: time.Now().Add(time.Hour)}
	}
	r, err := agent.ParseReviewer([]byte(res))
	if err == nil {
		// Write it where the pipeline reads it back after a restart.
		os.WriteFile(filepath.Join(s.TmpDir, "review.json"), []byte(res), 0o600)
	}
	return r, out, err
}

func (a *fakeAgent) implement(_ context.Context, s agent.Spec, resultFile string) (*agent.ImplementerResult, *agent.Outcome, error) {
	a.calls = append(a.calls, s)
	out := &agent.Outcome{SessionID: fmt.Sprintf("sess-%d", len(a.calls))}
	res := a.act(len(a.calls), s.Dir)
	if res == "" {
		return nil, out, &agent.LimitError{Status: "rejected", ResetsAt: time.Now().Add(time.Hour)}
	}
	r, err := agent.ParseImplementer([]byte(res))
	return r, out, err
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitx.Env(""), "GIT_AUTHOR_NAME=bot", "GIT_AUTHOR_EMAIL=b@b", "GIT_COMMITTER_NAME=bot", "GIT_COMMITTER_EMAIL=b@b")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--quiet", "-m", "add "+name)
}

const doneResult = `{"status":"done","summary":"Added login.","verification":"go test","assumptions":["email only"]}`

const (
	approve        = `{"verdict":"approve","summary":"Looks right.","comments":[{"path":"login.go","line":1,"severity":"nit","body":"name"}]}`
	requestChanges = `{"verdict":"request_changes","summary":"Missing tests.","comments":[{"path":"login.go","line":1,"severity":"must","body":"add a test"}]}`
)

type harness struct {
	t        *testing.T
	ctx      context.Context
	env      *Env
	p        *Project
	upstream string
	lin      *fakeLinear
	prs      *fakePRs
	agent    *fakeAgent
	events   *recorder
	issue    linear.Issue
	// kb is the frozen project context handed to the pickup.
	kb tmpl.Context
}

func newHarness(t *testing.T, cfg string) *harness {
	t.Helper()
	ctx := context.Background()
	up := upstream{t, t.TempDir()}
	gitIn(t, up.dir, "init", "--quiet", "--initial-branch=main")
	if cfg == "" {
		cfg = "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\n"
	}
	// CI runs on pull requests, so a PR is expected to get checks.
	os.MkdirAll(filepath.Join(up.dir, ".github", "workflows"), 0o755)
	os.WriteFile(filepath.Join(up.dir, ".github", "workflows", "ci.yml"), []byte("on: [pull_request]\n"), 0o644)
	up.commit(cfg)
	checkPoll, checkSettle = time.Millisecond, time.Millisecond

	home := config.Home(t.TempDir())
	repo, err := gitx.CloneBare(ctx, "file://"+up.dir, home.Project("demo").Repo(), "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "gosha.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	commit, _ := repo.RevParse(ctx, "origin/main")
	pc, _, err := config.ParseProject([]byte(cfg), commit)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t: t, ctx: ctx, upstream: up.dir,
		lin: &fakeLinear{}, prs: &fakePRs{prs: map[int]*github.PR{}}, events: &recorder{},
		p:     &Project{Name: "demo", Config: pc, Repo: repo, Base: "main", Commit: commit},
		issue: linear.Issue{ID: "i-12", Identifier: "NXT-12", Number: 12, Title: "Add login", URL: "https://linear.app/x/NXT-12"},
	}
	h.agent = &fakeAgent{t: t}
	h.env = &Env{Home: home, Global: &config.Global{}, Store: st, Notify: h.events,
		Log: slog.New(slog.DiscardHandler), Redactor: config.NewRedactor(), Implement: h.agent.implement, Review: h.agent.reviewer}
	return h
}

// pickup runs one pickup of the ticket as the poller would see it. Being
// stopped while waiting for checks (no h.prs.checks) isn't an error.
func (h *harness) pickup(comments ...linear.Comment) error {
	h.t.Helper()
	run, err := h.env.Store.OpenRun(h.ctx, "demo", h.issue.Identifier)
	if err != nil {
		h.t.Fatal(err)
	}
	delivered, err := h.env.Store.LastDelivered(h.ctx, "demo", h.issue.Identifier)
	if err != nil {
		h.t.Fatal(err)
	}
	cur, _ := h.env.Store.CommentCursor(h.ctx, "demo", h.issue.Identifier)
	h.lin.ticket = comments
	c := Candidate{Issue: h.issue, Comments: comments, NewComments: NewHumanComments(comments, cur), OpenRun: run, Delivered: delivered}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	h.prs.stop, h.prs.stopped = cancel, false
	err = h.env.handle(ctx, h.p, states, h.lin, h.prs, agent.Identity{Name: "goshabot[bot]", Email: "1+goshabot[bot]@users.noreply.github.com"}, h.kb, c)
	if h.prs.stopped && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// lastRun is the ticket's newest run, open or finished.
func (h *harness) lastRun() *store.Run {
	h.t.Helper()
	r, err := h.env.Store.LastRun(h.ctx, "demo", h.issue.Identifier)
	if err != nil || r == nil {
		h.t.Fatalf("last run: %v, %v", r, err)
	}
	return r
}

func (h *harness) run() *store.Run {
	h.t.Helper()
	r, err := h.env.Store.OpenRun(h.ctx, "demo", h.issue.Identifier)
	if err != nil || r == nil {
		h.t.Fatalf("open run: %v, %v", r, err)
	}
	return r
}

func (h *harness) onUpstream(branch string) bool {
	return exec.Command("git", "-C", h.upstream, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

func TestTicketToPR(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(_ int, dir string) string { commitFile(t, dir, "login.go"); return doneResult }
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepChecks || r.PRNumber != 1 || r.Branch != "gosha/NXT-12-add-login" || r.ImplSession != "sess-1" || Parked(r) || r.ReviewRound != 1 {
		t.Errorf("run = %+v", r)
	}
	if h.lin.state() != states.InProgress.ID || len(h.lin.attachments) != 1 || len(h.lin.comments) != 0 {
		t.Errorf("linear = %+v", h.lin)
	}
	if !h.onUpstream(r.Branch) {
		t.Error("branch not pushed")
	}
	s := h.agent.calls[0]
	if s.Resume != "" || s.Dir != filepath.Join(h.env.Home.Project("demo").Worktrees(), "NXT-12") ||
		!strings.Contains(s.Prompt, "# NXT-12: Add login") || s.Git.Name != "goshabot[bot]" {
		t.Errorf("spec = %+v", s)
	}
	if _, err := os.Stat(s.SettingsFile); err != nil {
		t.Errorf("settings: %v", err)
	}
	// The reviewer ran without edit tools in its own throwaway clone.
	rs := h.agent.reviews[0]
	if !slices.Equal(rs.DisallowedTools, agent.ReviewerDisallowed) || rs.Resume != "" ||
		rs.Dir != s.Dir+"-review" || !strings.Contains(rs.Prompt, "+login.go") || !strings.Contains(rs.Transcript, "2-review.jsonl") {
		t.Errorf("reviewer spec = %+v", rs)
	}
	if _, err := os.Stat(rs.Dir); !os.IsNotExist(err) {
		t.Errorf("review clone left behind: %v", err)
	}
	head := gitIn(t, h.upstream, "rev-parse", r.Branch)
	if len(h.prs.reviews) != 1 || h.prs.reviews[0].sha != head || !strings.Contains(h.prs.reviews[0].body, "✅ **Approved** · Gosha review 1/3") ||
		h.prs.reviews[0].comments[0].Body != "**nit:** name" {
		t.Errorf("reviews = %+v", h.prs.reviews)
	}
	ev := strings.Join(h.events.take(), "\n")
	for _, want := range []string{"picked up (fresh)", "implemented · +1 −0 in 1 files", "PR #1 opened", "review 1/3 ✅ · 0 must, 0 should, 1 nit"} {
		if !strings.Contains(ev, want) {
			t.Errorf("events missing %q:\n%s", want, ev)
		}
	}
}

func TestAgentBlockedThenResume(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(call int, dir string) string {
		if call == 1 {
			commitFile(t, dir, "partial.go")
			return `{"status":"blocked","blocked_reason":"Which login provider?","questions":["Google or GitHub?"]}`
		}
		if _, err := os.Stat(filepath.Join(dir, "partial.go")); err != nil {
			t.Error("resume lost the checkout")
		}
		commitFile(t, dir, "login.go")
		return doneResult
	}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepBlocked || h.lin.state() != states.Blocked.ID || len(h.lin.comments) != 1 ||
		!strings.Contains(h.lin.comments[0], "Which login provider?") || !strings.Contains(h.lin.comments[0], "Google or GitHub?") {
		t.Fatalf("after block: run %+v, linear %+v", r, h.lin)
	}
	answer := linear.Comment{ID: "c1", Body: "GitHub.", Author: "Ann", Human: true, CreatedAt: time.Now()}
	if err := h.pickup(answer); err != nil {
		t.Fatal(err)
	}
	r = h.run()
	if r.Step != StepChecks || r.Branch != "gosha/NXT-12-add-login" || r.AgentRuns != 3 {
		t.Errorf("run = %+v", r)
	}
	s := h.agent.calls[1]
	if s.Resume != "sess-1" || !strings.Contains(s.Prompt, "GitHub.") {
		t.Errorf("resume spec: resume %q, prompt has answer %v", s.Resume, strings.Contains(s.Prompt, "GitHub."))
	}
	// The same comment is not an instruction twice.
	cur, _ := h.env.Store.CommentCursor(h.ctx, "demo", "NXT-12")
	if cur == nil || cur.CommentID != "c1" {
		t.Errorf("cursor = %+v", cur)
	}
}

func TestNoChanges(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(int, string) string { return doneResult }
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if r := h.run(); r.Step != StepBlocked || !strings.Contains(r.BlockedReason, "No changes produced") {
		t.Errorf("run = %+v", r)
	}
}

func TestWorkflowPushRejected(t *testing.T) {
	h := newHarness(t, "")
	// Stand in for GitHub's refusal (SPIKES §3).
	rp := filepath.Join(t.TempDir(), "receive-pack")
	os.WriteFile(rp, []byte("#!/bin/sh\necho \"refusing to allow a GitHub App to create or update workflow '.github/workflows/ci.yml' without 'workflows' permission\" >&2\nexit 1\n"), 0o755)
	gitIn(t, h.p.Repo.Dir, "config", "remote.origin.receivepack", rp)
	h.agent.act = func(_ int, dir string) string { commitFile(t, dir, "ci.yml"); return doneResult }
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepBlocked || !strings.Contains(r.BlockedReason, ".github/workflows/") || len(h.lin.attachments) != 0 {
		t.Errorf("run = %+v", r)
	}
	if !strings.Contains(h.lin.comments[0], "while pushing the branch") {
		t.Errorf("comment = %s", h.lin.comments[0])
	}
}

func TestReadyOnOpenPRReviewsAgain(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(_ int, dir string) string { commitFile(t, dir, "login.go"); return doneResult }
	h.pickup()
	// No new comments anywhere: a fresh review of the same head.
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if r := h.run(); r.Step != StepChecks || r.ReviewRound != 1 || len(h.agent.calls) != 1 || len(h.prs.reviews) != 2 {
		t.Errorf("run = %+v, %d agent calls, %d reviews", r, len(h.agent.calls), len(h.prs.reviews))
	}
}

func TestFollowUpAfterMerge(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(call int, dir string) string { commitFile(t, dir, fmt.Sprintf("f%d.go", call)); return doneResult }
	h.pickup()
	first := h.run()
	h.prs.prs[first.PRNumber].State, h.prs.prs[first.PRNumber].Merged = "closed", true
	if err := h.pickup(linear.Comment{ID: "c1", Body: "also log out", Human: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.ID == first.ID || r.Mode != ModeFollowUp || r.Branch != "gosha/NXT-12-add-login-2" || r.Step != StepChecks || r.PRNumber != 2 {
		t.Errorf("follow-up run = %+v", r)
	}
	if h.agent.calls[1].Resume != "" {
		t.Error("follow-up resumed the old session")
	}
}

func TestRestart(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(call int, dir string) string { commitFile(t, dir, fmt.Sprintf("f%d.go", call)); return doneResult }
	h.pickup()
	first := h.run()
	if err := h.pickup(linear.Comment{ID: "c1", Body: "/restart", Human: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.ID == first.ID || r.Mode != ModeRestart || r.Branch != first.Branch || r.Step != StepChecks {
		t.Errorf("restarted run = %+v", r)
	}
	if len(h.prs.closed) != 1 || h.prs.closed[0] != first.PRNumber {
		t.Errorf("closed = %v", h.prs.closed)
	}
	// The new branch has only the new work.
	if _, err := os.Stat(filepath.Join(h.agent.calls[1].Dir, "f1.go")); err == nil {
		t.Error("restart kept the old work")
	}
}

func TestBranchNotOwned(t *testing.T) {
	h := newHarness(t, "")
	// Someone else's branch with Gosha's name.
	gitIn(t, h.upstream, "branch", "gosha/NXT-12-add-login")
	if err := h.p.Repo.Fetch(h.ctx, ""); err != nil {
		t.Fatal(err)
	}
	h.agent.act = func(_ int, dir string) string { commitFile(t, dir, "login.go"); return doneResult }
	h.pickup()
	if r := h.run(); r.Branch != "gosha/NXT-12-add-login-2" {
		t.Errorf("branch = %q", r.Branch)
	}
}

func TestUsageLimitLeavesTicketInProgress(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(int, string) string { return "" }
	var le *agent.LimitError
	if err := h.pickup(); !errors.As(err, &le) {
		t.Fatalf("err = %v", err)
	}
	if r := h.run(); r.Step != StepImplement || h.lin.state() != states.InProgress.ID || len(h.lin.comments) != 0 {
		t.Errorf("run %+v, linear %+v", r, h.lin)
	}
}

func TestSetupFailureBlocks(t *testing.T) {
	h := newHarness(t, "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\nagent:\n  setup: [\"echo installing\", \"echo broken >&2; exit 2\"]\n")
	h.agent.act = func(int, string) string { t.Error("agent ran after a failed setup"); return doneResult }
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepBlocked || !strings.Contains(r.BlockedReason, "exit 2") || !strings.Contains(r.BlockedReason, "broken") {
		t.Errorf("run = %+v", r)
	}
}

func TestInterruptedRunKeepsComments(t *testing.T) {
	h := newHarness(t, "")
	note := linear.Comment{ID: "c1", Body: "Use OAuth.", Author: "Ann", Human: true, CreatedAt: time.Now()}
	h.agent.act = func(call int, dir string) string {
		if call == 1 {
			return "" // usage limit
		}
		commitFile(t, dir, "login.go")
		return doneResult
	}
	var le *agent.LimitError
	if err := h.pickup(note); !errors.As(err, &le) {
		t.Fatalf("err = %v", err)
	}
	if cur, _ := h.env.Store.CommentCursor(h.ctx, "demo", "NXT-12"); cur != nil {
		t.Fatalf("cursor moved before the agent had the comments: %+v", cur)
	}
	// After the limit resets, the run continues with the same instructions.
	if err := h.pickup(note); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.agent.calls[1].Prompt, "Use OAuth.") {
		t.Error("the continued run lost the comment")
	}
	if r := h.run(); r.Step != StepChecks {
		t.Errorf("run = %+v", r)
	}
}
