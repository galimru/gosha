package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func commitIn(t *testing.T, dir, file, content string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "--quiet", "-m", "change " + file}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(Env(""), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestCheckoutRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, url := upstream(t)
	r, err := CloneBare(ctx, url, filepath.Join(t.TempDir(), "repo"), "")
	if err != nil {
		t.Fatal(err)
	}
	const branch = "gosha/NXT-1-x"
	dir := filepath.Join(t.TempDir(), "worktrees", "NXT-1")
	fresh, err := r.CloneCheckout(ctx, dir, "trunk", branch)
	if err != nil || !fresh {
		t.Fatalf("CloneCheckout = %v, %v", fresh, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, ".gosha", "config.yaml")); string(data) != "name: demo\n" {
		t.Errorf("checkout content = %q", data)
	}
	// The checkout shares nothing with the bare clone.
	if _, err := os.Stat(filepath.Join(dir, ".git", "objects", "info", "alternates")); err == nil {
		t.Error("checkout uses alternates")
	}

	if n, err := r.CommitsAhead(ctx, "trunk", branch); err == nil {
		t.Errorf("no branch yet, CommitsAhead = %d", n)
	}
	commitIn(t, dir, "a.txt", "one\ntwo\n")
	commitIn(t, dir, "b.txt", "three\n")
	if err := r.FetchFrom(ctx, dir, branch); err != nil {
		t.Fatal(err)
	}
	if n, err := r.CommitsAhead(ctx, "trunk", branch); err != nil || n != 2 {
		t.Errorf("CommitsAhead = %d, %v", n, err)
	}
	if d, err := r.Diff(ctx, "trunk", branch); err != nil || d != (DiffStat{Files: 2, Added: 3}) {
		t.Errorf("Diff = %+v, %v", d, err)
	}

	if ok, _ := r.RemoteBranchExists(ctx, branch); ok {
		t.Error("branch on origin before push")
	}
	if err := r.Push(ctx, "", branch); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", src, "rev-parse", "--verify", "refs/heads/"+branch).CombinedOutput(); err != nil {
		t.Errorf("not pushed: %s", out)
	}
	if ok, _ := r.RemoteBranchExists(ctx, branch); !ok {
		t.Error("origin ref not updated after push")
	}

	// A lost checkout is rebuilt from Gosha's collected work, not from base.
	os.RemoveAll(dir)
	if _, err := r.CloneCheckout(ctx, dir, "trunk", branch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Errorf("rebuilt checkout lost work: %v", err)
	}
	// An existing checkout is reused untouched.
	os.WriteFile(filepath.Join(dir, "dirty"), nil, 0o644)
	if fresh, err := r.CloneCheckout(ctx, dir, "trunk", branch); err != nil || fresh {
		t.Errorf("reuse = %v, %v", fresh, err)
	}

	if err := r.DeleteBranch(ctx, "", branch); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", src, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).CombinedOutput(); err == nil {
		t.Errorf("still on origin: %s", out)
	}
	for _, f := range []func(context.Context, string) (bool, error){r.RemoteBranchExists, r.LocalBranchExists} {
		if ok, err := f(ctx, branch); ok || err != nil {
			t.Errorf("after delete: %v, %v", ok, err)
		}
	}
	if err := r.DeleteBranch(ctx, "", branch); err != nil {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestFetchFromRefusesGitFile(t *testing.T) {
	ctx := context.Background()
	_, url := upstream(t)
	r, err := CloneBare(ctx, url, filepath.Join(t.TempDir(), "repo"), "")
	if err != nil {
		t.Fatal(err)
	}
	// An agent could replace .git with a "gitdir:" file pointing elsewhere.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+r.Dir+"\n"), 0o644)
	if err := r.FetchFrom(ctx, dir, "x"); !errors.Is(err, ErrNotCheckout) {
		t.Errorf("FetchFrom = %v", err)
	}
}

func TestEnvDisablesHooks(t *testing.T) {
	if env := Env(""); !slices.Contains(env, "GIT_CONFIG_KEY_0=core.hooksPath") || !slices.Contains(env, "GIT_CONFIG_VALUE_0="+os.DevNull) {
		t.Errorf("hooks not disabled: %v", env)
	}
	if env := Env("ghs_x"); !slices.Contains(env, "GIT_CONFIG_COUNT=2") || !slices.Contains(env, "GIT_CONFIG_KEY_1=core.hooksPath") {
		t.Errorf("hooks not disabled with a token: %v", env)
	}
}
