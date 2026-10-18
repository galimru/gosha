package gitx

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// upstream makes a non-bare repo with one commit on branch "trunk" and
// returns its file:// URL.
func upstream(t *testing.T) (dir, url string) {
	t.Helper()
	dir = t.TempDir()
	sh := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(Env(""), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sh("init", "--quiet", "--initial-branch=trunk")
	os.MkdirAll(filepath.Join(dir, ".gosha"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gosha", "config.yaml"), []byte("name: demo\n"), 0o644)
	sh("add", ".")
	sh("commit", "--quiet", "-m", "init")
	return dir, "file://" + dir
}

func TestCloneShowFetch(t *testing.T) {
	ctx := context.Background()
	src, url := upstream(t)
	r, err := CloneBare(ctx, url, filepath.Join(t.TempDir(), "repo"), "")
	if err != nil {
		t.Fatal(err)
	}
	def, err := r.DefaultBranch(ctx)
	if err != nil || def != "trunk" {
		t.Fatalf("DefaultBranch = %q, %v", def, err)
	}
	data, err := r.Show(ctx, "origin/trunk", ".gosha/config.yaml")
	if err != nil || string(data) != "name: demo\n" {
		t.Fatalf("Show = %q, %v", data, err)
	}
	if _, err := r.Show(ctx, "origin/trunk", "missing.yaml"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing path: %v", err)
	}
	if _, err := r.Show(ctx, "origin/nope", ".gosha/config.yaml"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("missing ref should be a different error: %v", err)
	}

	// A new upstream commit shows up after Fetch.
	os.WriteFile(filepath.Join(src, ".gosha", "config.yaml"), []byte("name: demo2\n"), 0o644)
	cmd := exec.Command("git", "commit", "--quiet", "-am", "two")
	cmd.Dir = src
	cmd.Env = append(Env(""), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := r.Fetch(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if data, _ := r.Show(ctx, "origin/trunk", ".gosha/config.yaml"); string(data) != "name: demo2\n" {
		t.Errorf("after fetch: %q", data)
	}
	if u, _ := r.OriginURL(ctx); u != url {
		t.Errorf("origin = %q", u)
	}
}

func TestEnvToken(t *testing.T) {
	env := Env("ghs_secret")
	want := "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_secret"))
	if !slices.Contains(env, want) || !slices.Contains(env, "GIT_CONFIG_KEY_0="+extraHeaderKey) {
		t.Errorf("token header missing from env: %v", env)
	}
	if !slices.Contains(env, "GIT_CONFIG_GLOBAL=/dev/null") || !slices.Contains(env, "GIT_CONFIG_NOSYSTEM=1") {
		t.Errorf("user config not disabled: %v", env)
	}
	for _, e := range env {
		if strings.Contains(e, "ghs_secret") {
			t.Errorf("raw token in env: %s", e)
		}
	}
}

func TestTokenNotInArgsOrConfig(t *testing.T) {
	ctx := context.Background()
	_, url := upstream(t)
	r, err := CloneBare(ctx, url, filepath.Join(t.TempDir(), "repo"), "ghs_secret")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(r.Dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_secret"))
	if strings.Contains(string(cfg), "ghs_secret") || strings.Contains(string(cfg), b64) || strings.Contains(string(cfg), "extraheader") {
		t.Errorf("token persisted in git config:\n%s", cfg)
	}
	var gerr *Error
	_, err = run(ctx, r.Dir, "ghs_secret", "no-such-command")
	if !errors.As(err, &gerr) || slices.ContainsFunc(gerr.Args, func(a string) bool { return strings.Contains(a, "ghs_secret") }) {
		t.Errorf("unexpected: %v", err)
	}
}

func TestParseGitHubURL(t *testing.T) {
	good := []string{
		"https://github.com/galimru/gosha",
		"https://github.com/galimru/gosha.git",
		"https://github.com/galimru/gosha/",
		"git@github.com:galimru/gosha.git",
		"git@github.com:galimru/gosha",
		"ssh://git@github.com/galimru/gosha.git",
		"galimru/gosha",
	}
	for _, in := range good {
		r, err := ParseGitHubURL(in)
		if err != nil || r.URL() != "https://github.com/galimru/gosha.git" {
			t.Errorf("ParseGitHubURL(%q) = %v, %v", in, r, err)
		}
	}
	for _, in := range []string{"https://gitlab.com/a/b", "http://github.com/a/b", "https://github.com/a", "https://github.com/a/b/c", "nonsense"} {
		if _, err := ParseGitHubURL(in); err == nil {
			t.Errorf("ParseGitHubURL(%q): expected an error", in)
		}
	}
}

func TestCheckBranchName(t *testing.T) {
	for _, ok := range []string{"gosha/NXT-12-add-login", "feature/nxt-12", "a.b/c"} {
		if err := CheckBranchName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "@", "-x", "/x", "x/", "x.", "a..b", "a//b", "a@{b", "a b", "a~b", "a:b", "a/.b", "a.lock", "a/b.lock/c", "a\\b"} {
		if err := CheckBranchName(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}
