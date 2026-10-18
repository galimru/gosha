package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/galimru/gosha/internal/gitx"
	"github.com/galimru/gosha/internal/tmpl"
)

func TestParseReviewer(t *testing.T) {
	good := []string{
		`{"verdict":"approve","summary":"fine"}`,
		`{"verdict":"request_changes","summary":"x","comments":[{"path":"a.go","line":3,"severity":"must","body":"bug"},{"path":"b.go","severity":"nit","body":"name"}]}`,
	}
	for _, g := range good {
		if _, err := ParseReviewer([]byte(g)); err != nil {
			t.Errorf("%s: %v", g, err)
		}
	}
	bad := []string{
		`{"verdict":"lgtm","summary":"x"}`,
		`{"verdict":"approve"}`,
		`{"verdict":"approve","summary":"x","comments":[{"line":1,"severity":"must","body":"b"}]}`,
		`{"verdict":"approve","summary":"x","comments":[{"path":"a","severity":"must"}]}`,
		`{"verdict":"approve","summary":"x","comments":[{"path":"a","severity":"blocker","body":"b"}]}`,
		`{"verdict":"approve","summary":"x","comments":[{"path":"a","line":-1,"severity":"nit","body":"b"}]}`,
		`{"verdict":"approve","summary":"x","extra":1}`,
	}
	for _, b := range bad {
		if _, err := ParseReviewer([]byte(b)); err == nil {
			t.Errorf("%s: expected an error", b)
		}
	}
	r, _ := ParseReviewer([]byte(good[1]))
	if r.Count(SeverityMust) != 1 || r.Count(SeverityNit) != 1 || r.Count(SeverityShould) != 0 {
		t.Errorf("counts wrong: %+v", r)
	}
}

func TestReviewRun(t *testing.T) {
	res := filepath.Join(t.TempDir(), "result.json")
	bin, dir := fakeClaude(t, `echo '{"verdict":"approve","summary":"looks right"}' > `+res+`
echo '{"type":"result","subtype":"success","is_error":false,"session_id":"sess-r"}'`)
	s := spec(t, bin)
	s.DisallowedTools = ReviewerDisallowed
	r, _, err := Review(context.Background(), s, res)
	if err != nil || r.Verdict != VerdictApprove {
		t.Fatalf("Review = %+v, %v", r, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(args), "--disallowedTools Edit,Write,NotebookEdit") {
		t.Errorf("args = %s", args)
	}
	if got, err := ReadReview(res); err != nil || got.Summary != "looks right" {
		t.Errorf("ReadReview = %+v, %v", got, err)
	}
}

func TestReviewerSettings(t *testing.T) {
	home := t.TempDir()
	p := Paths{
		Home: home, Checkout: filepath.Join(home, "projects/demo/worktrees/NXT-1-review"),
		RunDir: filepath.Join(home, "projects/demo/runs/NXT-1/4"), UserHome: t.TempDir(),
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	data, err := Settings(p, RoleReviewer, true)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Permissions struct{ Allow []string }
		Sandbox     struct {
			Filesystem struct{ AllowRead, AllowWrite []string }
		}
	}
	json.Unmarshal(data, &s)
	if slices.ContainsFunc(s.Permissions.Allow, func(a string) bool { return strings.HasPrefix(a, "Edit") }) {
		t.Errorf("reviewer may edit: %v", s.Permissions.Allow)
	}
	if !slices.Contains(s.Permissions.Allow, "Bash") || !slices.Contains(s.Permissions.Allow, "Read") {
		t.Errorf("reviewer can't run or read: %v", s.Permissions.Allow)
	}
	// Its Bash can read the clone but write only its run dir.
	if fs := s.Sandbox.Filesystem; !slices.Contains(fs.AllowRead, p.Checkout) || !slices.Equal(fs.AllowWrite, []string{p.RunDir}) {
		t.Errorf("reviewer sandbox: %+v", fs)
	}
}

func TestReviewAndAddressPrompts(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", "--initial-branch=main"}, {"commit", "--quiet", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(gitx.Env(""), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	repo := gitx.Open(filepath.Join(dir, ".git"))
	d := tmpl.Data{
		Project: "demo", Branch: "gosha/NXT-12-add-login", BaseBranch: "main", ResultFile: "/r/result.json", Round: 2,
		Ticket: tmpl.Ticket{ID: "NXT-12", Title: "Add login", Description: "Users log in.",
			Comments: []tmpl.Comment{{Author: "Ann", Body: "Use OAuth."}}},
		Feedback: tmpl.Feedback{Review: "- `a.go:3` (must): handle the error", Previous: "Reviewer asked for X."},
	}
	got, err := RenderPrompt(context.Background(), repo, "HEAD", "review.md", d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"review round 2", "# NXT-12: Add login", "Reviewer asked for X.",
		"git diff origin/main...HEAD", "`/r/result.json`", "request_changes"} {
		if !strings.Contains(got, want) {
			t.Errorf("review prompt missing %q:\n%s", want, got)
		}
	}
	got, err = RenderPrompt(context.Background(), repo, "HEAD", "address.md", d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"round 2", "Use OAuth.", "handle the error", "`gosha/NXT-12-add-login`", "`/r/result.json`"} {
		if !strings.Contains(got, want) {
			t.Errorf("address prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "# Failed checks") || strings.Contains(got, "# Merge conflicts") {
		t.Error("CI sections without CI feedback")
	}
	d.Feedback = tmpl.Feedback{CI: "### test\n\nFAIL TestLogin", Conflicts: "- `a.go`"}
	got, _ = RenderPrompt(context.Background(), repo, "HEAD", "address.md", d)
	for _, want := range []string{"# Failed checks", "FAIL TestLogin", "git merge origin/main", "- `a.go`"} {
		if !strings.Contains(got, want) {
			t.Errorf("address prompt missing %q:\n%s", want, got)
		}
	}
	// Round 1: no previous-round section.
	d.Round, d.Feedback.Previous = 1, ""
	got, _ = RenderPrompt(context.Background(), repo, "HEAD", "review.md", d)
	if strings.Contains(got, "The previous round") {
		t.Error("previous-round section in round 1")
	}
}
