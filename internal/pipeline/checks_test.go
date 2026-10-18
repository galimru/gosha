package pipeline

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/agent"
	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/gitx"
)

func green(int, string) []github.Check {
	return []github.Check{{Name: "test", Kind: github.KindCheckRun, Status: "completed", Conclusion: "success"}}
}

func red(int, string) []github.Check {
	return []github.Check{{Name: "test", Kind: github.KindCheckRun, Status: "completed", Conclusion: "failure", URL: "https://ci/1", JobID: 7}}
}

// failOn fails the checks on the first head they see and passes later ones.
func failOn() func(int, string) []github.Check {
	first := ""
	return func(call int, sha string) []github.Check {
		if first == "" {
			first = sha
		}
		if sha == first {
			return red(call, sha)
		}
		return green(call, sha)
	}
}

func TestChecksGreenMerges(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = commitEach(t)
	h.prs.checks = green
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.lastRun()
	if r.Outcome != OutcomeDelivered || r.Step != StepMerge || r.MergeSHA == "" || r.CheckRound != 0 {
		t.Fatalf("run = %+v", r)
	}
	if _, err := h.env.Store.OpenRun(h.ctx, "demo", "NXT-12"); err != nil {
		t.Fatal(err)
	}
	if len(h.prs.merged) != 1 || h.prs.merged[0].sha != r.CheckedSHA || h.prs.merged[0].method != "squash" || h.prs.merged[0].title != "NXT-12: Add login (#1)" {
		t.Errorf("merged = %+v, checked %s", h.prs.merged, r.CheckedSHA)
	}
	if h.onUpstream(r.Branch) {
		t.Error("branch not deleted")
	}
	if _, err := os.Stat(h.env.Home.Workspace("demo", "NXT-12").Dir); !os.IsNotExist(err) {
		t.Errorf("workspace left behind: %v", err)
	}
	if h.lin.state() != states.InReview.ID || len(h.lin.comments) != 1 {
		t.Fatalf("linear = %+v", h.lin)
	}
	for _, want := range []string{"**Delivered:** [PR #1]", "+1 −0 in 1 files", "Added login.", "Checks passed: `test`", "Review: review 1/3 ✅", "Verification: go test", "**Assumptions**\n- email only"} {
		if !strings.Contains(h.lin.comments[0], want) {
			t.Errorf("Delivered comment missing %q:\n%s", want, h.lin.comments[0])
		}
	}
	ev := strings.Join(h.events.take(), "\n")
	for _, want := range []string{"checks ✅ 1/1", "squash-merged merge-", "delivered · In Review"} {
		if !strings.Contains(ev, want) {
			t.Errorf("events missing %q:\n%s", want, ev)
		}
	}
}

func TestNoChecksExpected(t *testing.T) {
	h := newHarness(t, "")
	os.RemoveAll(filepath.Join(h.upstream, ".github"))
	gitIn(t, h.upstream, "commit", "--quiet", "-am", "no ci")
	h.p.Repo.Fetch(h.ctx, "")
	h.agent.act = commitEach(t)
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if r := h.lastRun(); r.Outcome != OutcomeDelivered || h.prs.checkCalls != 0 {
		t.Errorf("run = %+v, %d check calls", r, h.prs.checkCalls)
	}
	if !strings.Contains(h.lin.comments[0], "Checks: none expected") {
		t.Errorf("comment = %s", h.lin.comments[0])
	}
}

func TestFailedCheckFixed(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = commitEach(t)
	h.prs.checks = failOn()
	h.prs.logs = map[int64]string{7: jobLog}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.lastRun()
	if r.Outcome != OutcomeDelivered || r.CheckRound != 1 || r.ReviewRound != 1 || len(h.agent.calls) != 2 || len(h.prs.reviews) != 2 {
		t.Fatalf("run = %+v, %d impl calls, %d reviews", r, len(h.agent.calls), len(h.prs.reviews))
	}
	fix := h.agent.calls[1]
	for _, want := range []string{"# Failed checks", "## test (failure)", "--- FAIL: TestLogin", "https://ci/1"} {
		if !strings.Contains(fix.Prompt, want) {
			t.Errorf("fix prompt missing %q", want)
		}
	}
	if strings.Contains(fix.Prompt, "Post job cleanup") || fix.Resume != "sess-1" {
		t.Errorf("fix spec: resume %q", fix.Resume)
	}
	if !strings.Contains(h.prs.reviews[1].body, "re-review after fixes") || h.prs.merged[0].sha != r.CheckedSHA {
		t.Errorf("re-review %q, merged %+v", h.prs.reviews[1].body, h.prs.merged)
	}
	if !strings.Contains(h.prs.comments[0], "**Check round 1: fixed.**") {
		t.Errorf("PR comments = %v", h.prs.comments)
	}
	ev := strings.Join(h.events.take(), "\n")
	for _, want := range []string{"checks ❌ test → fixing 1/3", "fixed (check round 1/3)", "re-review after fixes ✅", "checks ✅ 1/1"} {
		if !strings.Contains(ev, want) {
			t.Errorf("events missing %q:\n%s", want, ev)
		}
	}
}

func TestCheckRoundsExhausted(t *testing.T) {
	h := newHarness(t, "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\nchecks:\n  max_rounds: 1\n")
	h.agent.act = commitEach(t)
	h.prs.checks = red
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepBlocked || r.CheckRound != 1 || len(h.agent.calls) != 2 || len(h.prs.merged) != 0 {
		t.Fatalf("run = %+v, %d impl calls", r, len(h.agent.calls))
	}
	if c := h.lin.comments[0]; !strings.Contains(c, "while waiting for checks") || !strings.Contains(c, "after 1 fix rounds") || !strings.Contains(c, "https://ci/1") {
		t.Errorf("Blocked comment = %s", c)
	}
}

func TestFixWithoutCommitBlocks(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(call int, dir string) string {
		if call == 1 {
			commitFile(t, dir, "login.go")
			return doneResult
		}
		return `{"status":"done","summary":"The failure is a flaky network test."}`
	}
	h.prs.checks = red
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepBlocked || !strings.Contains(r.BlockedReason, "committed no fix") || !strings.Contains(r.BlockedReason, "flaky network test") {
		t.Errorf("run = %+v", r)
	}
	if !strings.Contains(h.lin.comments[0], "while fixing failed checks") {
		t.Errorf("comment = %s", h.lin.comments[0])
	}
}

func TestRequiredCheckNeverStarts(t *testing.T) {
	h := newHarness(t, "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\nchecks:\n  required: [test, lint]\n  start_grace: 20ms\n")
	h.agent.act = commitEach(t)
	h.prs.checks = green
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepBlocked || !strings.Contains(r.BlockedReason, "never started") || !strings.Contains(r.BlockedReason, "`lint`") ||
		len(h.agent.calls) != 1 || r.CheckRound != 0 {
		t.Errorf("run = %+v, %d impl calls", r, len(h.agent.calls))
	}
}

func TestChecksTimeOut(t *testing.T) {
	h := newHarness(t, "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\nchecks:\n  timeout: 20ms\n  max_rounds: 1\n")
	h.agent.act = commitEach(t)
	h.prs.checks = func(int, string) []github.Check {
		return []github.Check{{Name: "e2e", Kind: github.KindCheckRun, Status: "in_progress"}}
	}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.agent.calls[1].Prompt, "## e2e (timed out)") {
		t.Errorf("fix prompt = %s", h.agent.calls[1].Prompt)
	}
	if r := h.run(); r.Step != StepBlocked || !strings.Contains(r.BlockedReason, "`e2e`: timed out") {
		t.Errorf("run = %+v", r)
	}
}

func TestBaseMovedMergesCleanly(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = commitEach(t)
	var shas []string
	h.prs.checks = func(call int, sha string) []github.Check {
		if len(shas) == 0 || shas[len(shas)-1] != sha {
			shas = append(shas, sha)
		}
		if call == 1 {
			// Someone merges something else into main meanwhile.
			commitFile(t, h.upstream, "other.go")
		}
		return green(call, sha)
	}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.lastRun()
	if r.Outcome != OutcomeDelivered || len(shas) != 2 || h.prs.merged[0].sha != shas[1] || r.CheckRound != 0 {
		t.Fatalf("run = %+v, checked %v, merged %+v", r, shas, h.prs.merged)
	}
	// GitHub merged main into the branch, and Gosha took its merge.
	parents := gitIn(t, h.p.Repo.Dir, "log", "-1", "--format=%P", shas[1])
	if len(strings.Fields(parents)) != 2 || h.prs.updates != 1 {
		t.Errorf("merge commit parents %q, %d updates", parents, h.prs.updates)
	}
	if len(h.agent.calls) != 1 || len(h.prs.reviews) != 1 {
		t.Errorf("%d impl calls, %d reviews: a clean base merge needs neither", len(h.agent.calls), len(h.prs.reviews))
	}
}

func TestBaseConflictResolved(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(call int, dir string) string {
		if call == 1 {
			commitFile(t, dir, "login.go")
			return doneResult
		}
		// Resolve the conflict the way the prompt asks.
		cmd := exec.Command("git", "merge", "origin/main")
		cmd.Dir = dir
		cmd.Env = append(gitx.Env(""), "GIT_AUTHOR_NAME=bot", "GIT_AUTHOR_EMAIL=b@b", "GIT_COMMITTER_NAME=bot", "GIT_COMMITTER_EMAIL=b@b")
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("merge didn't conflict: %s", out)
		}
		os.WriteFile(filepath.Join(dir, "login.go"), []byte("both\n"), 0o644)
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "--quiet", "--no-edit")
		return `{"status":"done","summary":"Merged main, kept both."}`
	}
	h.prs.checks = func(call int, sha string) []github.Check {
		if call == 1 {
			os.WriteFile(filepath.Join(h.upstream, "login.go"), []byte("theirs\n"), 0o644)
			gitIn(t, h.upstream, "add", "-A")
			gitIn(t, h.upstream, "commit", "--quiet", "-m", "theirs")
		}
		return green(call, sha)
	}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.lastRun()
	if r.Outcome != OutcomeDelivered || r.CheckRound != 1 || len(h.agent.calls) != 2 || len(h.prs.reviews) != 2 {
		t.Fatalf("run = %+v, %d impl calls, %d reviews", r, len(h.agent.calls), len(h.prs.reviews))
	}
	fix := h.agent.calls[1]
	if !strings.Contains(fix.Prompt, "# Merge conflicts") || !strings.Contains(fix.Prompt, "git merge origin/main") || fix.Resume != "" {
		t.Errorf("fix spec: resume %q, prompt %s", fix.Resume, fix.Prompt)
	}
	if !strings.Contains(h.prs.comments[0], "merged `main` and resolved the conflicts") {
		t.Errorf("PR comments = %v", h.prs.comments)
	}
}

func TestMergeRefusedWhenHeadMoved(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = commitEach(t)
	h.prs.checks = green
	h.prs.mergeErr = github.ErrHeadMoved
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.run()
	if r.Step != StepBlocked || !strings.Contains(r.BlockedReason, "Someone else pushed") || r.MergeSHA != "" {
		t.Errorf("run = %+v", r)
	}
	if !strings.Contains(h.lin.comments[0], "while merging the pull request") {
		t.Errorf("comment = %s", h.lin.comments[0])
	}
}

func TestCrashAfterMerge(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = commitEach(t)
	h.pickup() // stopped while waiting for checks
	// Gosha merged it, then stopped before reporting.
	r := h.run()
	r.Step, r.MergeSHA = StepMerge, "abc1234"
	h.env.Store.UpdateRun(h.ctx, r)
	h.prs.prs[r.PRNumber].State, h.prs.prs[r.PRNumber].Merged = "closed", true
	h.issue.State = states.InProgress
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	last := h.lastRun()
	if last.ID != r.ID || last.Outcome != OutcomeDelivered || h.lin.state() != states.InReview.ID || len(h.agent.calls) != 1 {
		t.Errorf("run = %+v, linear %+v", last, h.lin)
	}
	// What the first pickup learned is still reported.
	if !strings.Contains(h.lin.comments[0], "is merged (`abc1234`)") || !strings.Contains(h.lin.comments[0], "Added login.") {
		t.Errorf("comment = %s", h.lin.comments[0])
	}
}

func TestInterruptedFixKeepsRound(t *testing.T) {
	h := newHarness(t, "")
	h.agent.act = func(call int, dir string) string {
		if call == 2 {
			return "" // usage limit during the fix
		}
		commitFile(t, dir, fmt.Sprintf("f%d.go", call))
		return doneResult
	}
	h.prs.checks = failOn()
	var le *agent.LimitError
	if err := h.pickup(); !errors.As(err, &le) {
		t.Fatalf("err = %v", err)
	}
	if r := h.run(); r.Step != StepFix || r.CheckRound != 1 || r.ChecksResult == "" {
		t.Fatalf("after limit: %+v", r)
	}
	h.issue.State = states.InProgress
	calls := h.prs.checkCalls
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	r := h.lastRun()
	if r.Outcome != OutcomeDelivered || r.CheckRound != 1 || !strings.Contains(h.agent.calls[2].Prompt, "## test (failure)") {
		t.Errorf("run = %+v", r)
	}
	// The fix continued from the saved feedback, without waiting for the
	// failed head's checks again: the next checks are on the fix.
	if h.prs.checkCalls-calls != 2 {
		t.Errorf("%d check calls after the restart", h.prs.checkCalls-calls)
	}
}

func TestWaitingForChecksDoesntSpendAgentTime(t *testing.T) {
	h := newHarness(t, "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\nagent:\n  timeout: 300ms\n")
	h.agent.act = commitEach(t)
	fail := failOn()
	h.prs.checks = func(call int, sha string) []github.Check {
		if call == 1 {
			time.Sleep(400 * time.Millisecond) // CI longer than the whole agent budget
		}
		return fail(call, sha)
	}
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	if r := h.lastRun(); r.Outcome != OutcomeDelivered {
		t.Errorf("run = %+v", r)
	}
}
