package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMergeBase(t *testing.T) {
	ctx := context.Background()
	src, url := upstream(t)
	r, err := CloneBare(ctx, url, filepath.Join(t.TempDir(), "repo"), "")
	if err != nil {
		t.Fatal(err)
	}
	const branch = "gosha/NXT-1-x"
	dir := filepath.Join(t.TempDir(), "NXT-1")
	if _, err := r.CloneCheckout(ctx, dir, "trunk", branch); err != nil {
		t.Fatal(err)
	}
	commitIn(t, dir, "a.txt", "mine\n")
	if err := r.FetchFrom(ctx, dir, branch); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.IsAncestor(ctx, "refs/remotes/origin/trunk", "refs/heads/"+branch); !ok || err != nil {
		t.Fatalf("IsAncestor before base moved = %v, %v", ok, err)
	}

	// The base moves without touching the branch's files: a clean merge.
	commitIn(t, src, "b.txt", "theirs\n")
	if err := r.Fetch(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.IsAncestor(ctx, "refs/remotes/origin/trunk", "refs/heads/"+branch); ok {
		t.Fatal("base moved but still an ancestor")
	}
	conflicts, err := r.MergeBase(ctx, "trunk", branch, Author{"gosha[bot]", "1+gosha[bot]@users.noreply.github.com"})
	if err != nil || conflicts != nil {
		t.Fatalf("clean MergeBase = %v, %v", conflicts, err)
	}
	if ok, _ := r.IsAncestor(ctx, "refs/remotes/origin/trunk", "refs/heads/"+branch); !ok {
		t.Error("base not merged")
	}
	out, _ := exec.Command("git", "-C", r.Dir, "log", "-1", "--format=%an <%ae>|%P|%s", "refs/heads/"+branch).Output()
	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if parts[0] != "gosha[bot] <1+gosha[bot]@users.noreply.github.com>" || len(strings.Fields(parts[1])) != 2 || parts[2] != "Merge trunk into "+branch {
		t.Errorf("merge commit = %q", out)
	}
	if _, err := r.Show(ctx, "refs/heads/"+branch, "b.txt"); err != nil {
		t.Errorf("merged tree lacks base's file: %v", err)
	}

	// Both sides change the same file: a conflict leaves the branch alone.
	head, _ := r.RevParse(ctx, "refs/heads/"+branch)
	commitIn(t, src, "a.txt", "theirs\n")
	if err := r.Fetch(ctx, ""); err != nil {
		t.Fatal(err)
	}
	conflicts, err = r.MergeBase(ctx, "trunk", branch, Author{"b", "b@b"})
	if err != nil || !slices.Equal(conflicts, []string{"a.txt"}) {
		t.Fatalf("conflicting MergeBase = %v, %v", conflicts, err)
	}
	if now, _ := r.RevParse(ctx, "refs/heads/"+branch); now != head {
		t.Error("a conflict moved the branch")
	}
}

func TestHasCIWorkflows(t *testing.T) {
	ctx := context.Background()
	src, url := upstream(t)
	r, err := CloneBare(ctx, url, filepath.Join(t.TempDir(), "repo"), "")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := r.HasCIWorkflows(ctx, "refs/remotes/origin/trunk"); ok || err != nil {
		t.Errorf("no workflows = %v, %v", ok, err)
	}
	os.MkdirAll(filepath.Join(src, ".github", "workflows"), 0o755)
	commitIn(t, src, ".github/workflows/deploy.yml", "on:\n  workflow_dispatch:\n")
	r.Fetch(ctx, "")
	if ok, err := r.HasCIWorkflows(ctx, "refs/remotes/origin/trunk"); ok || err != nil {
		t.Errorf("deploy only = %v, %v", ok, err)
	}
	commitIn(t, src, ".github/workflows/ci.yaml", "on: [push]\n")
	r.Fetch(ctx, "")
	if ok, err := r.HasCIWorkflows(ctx, "refs/remotes/origin/trunk"); !ok || err != nil {
		t.Errorf("ci = %v, %v", ok, err)
	}
}

func TestRunsOnPR(t *testing.T) {
	for in, want := range map[string]bool{
		"on: push\n": true,
		"on: [workflow_dispatch, pull_request]\n":   true,
		"on:\n  pull_request_target:\n":             true,
		"on:\n  push:\n    branches: [main]\n":      true,
		"on:\n  schedule:\n    - cron: '0 0 * *'\n": false,
		"on: workflow_dispatch\n":                   false,
		"on: [\n":                                   true,
	} {
		if got := RunsOnPR([]byte(in)); got != want {
			t.Errorf("RunsOnPR(%q) = %v", in, got)
		}
	}
}

func TestVersion(t *testing.T) {
	major, minor, err := Version(context.Background())
	if err != nil || major < 2 {
		t.Errorf("Version = %d.%d, %v", major, minor, err)
	}
}
