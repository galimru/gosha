package gitx

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsAncestorAndTakeOrigin(t *testing.T) {
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
	commitIn(t, src, "b.txt", "theirs\n")
	if err := r.Fetch(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.IsAncestor(ctx, "refs/remotes/origin/trunk", "refs/heads/"+branch); ok {
		t.Fatal("base moved but still an ancestor")
	}

	// GitHub merges the base into the branch; the bare clone takes it.
	if err := r.Push(ctx, "", branch); err != nil {
		t.Fatal(err)
	}
	gitC(t, src, "checkout", "--quiet", branch)
	gitC(t, src, "merge", "--quiet", "--no-edit", "trunk")
	gitC(t, src, "checkout", "--quiet", "trunk")
	if err := r.Fetch(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.TakeOrigin(ctx, branch); err != nil {
		t.Fatal(err)
	}
	local, _ := r.RevParse(ctx, "refs/heads/"+branch)
	remote, _ := r.RevParse(ctx, "refs/remotes/origin/"+branch)
	if local != remote {
		t.Errorf("branch %s, origin %s", local, remote)
	}
	if ok, _ := r.IsAncestor(ctx, "refs/remotes/origin/trunk", "refs/heads/"+branch); !ok {
		t.Error("base not merged")
	}
}

func gitC(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(Env(""), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
