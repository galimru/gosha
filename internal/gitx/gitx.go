// Package gitx runs the git CLI for Gosha's bare clones. Gosha's own token is
// injected per command and never stored, and the user's git
// config never applies.
package gitx

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// ErrNotFound means the ref exists but the path isn't in it.
var ErrNotFound = errors.New("not found")

// extraHeaderKey scopes the token to GitHub, the only host Gosha pushes to.
const extraHeaderKey = "http.https://github.com/.extraheader"

// Env is the environment for every git command: PATH and HOME only, the
// user's global and system config disabled, no prompts, no hooks, and the
// token (if any) as an HTTP header through GIT_CONFIG_* so it never reaches
// argv or a config file.
func Env(token string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LC_ALL=C",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	}
	var kv [][2]string
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		kv = append(kv, [2]string{extraHeaderKey, "AUTHORIZATION: basic " + basic})
	}
	// Gosha's own git commands never run hooks, whatever a repo's config says.
	kv = append(kv, [2]string{"core.hooksPath", os.DevNull})
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(kv)))
	for i, p := range kv {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, p[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, p[1]))
	}
	return env
}

// Error is a failed git command with its stderr.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

func run(ctx context.Context, dir, token string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = Env(token)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

// Repo is a bare clone with an origin remote.
type Repo struct{ Dir string }

// Open returns the bare clone at dir without checking it.
func Open(dir string) *Repo { return &Repo{Dir: dir} }

// CloneBare clones url into dir as a bare repo and maps origin's branches to
// refs/remotes/origin/*, so origin/<default> exists (SPIKES §3).
func CloneBare(ctx context.Context, url, dir, token string) (*Repo, error) {
	if _, err := run(ctx, "", token, "clone", "--bare", "--quiet", url, dir); err != nil {
		return nil, err
	}
	r := Open(dir)
	if _, err := r.git(ctx, "", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return nil, err
	}
	if err := r.Fetch(ctx, token); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Repo) git(ctx context.Context, token string, args ...string) ([]byte, error) {
	return run(ctx, r.Dir, token, args...)
}

// Fetch updates origin's branches and origin/HEAD.
func (r *Repo) Fetch(ctx context.Context, token string) error {
	if _, err := r.git(ctx, token, "fetch", "--quiet", "--prune", "origin"); err != nil {
		return err
	}
	_, err := r.git(ctx, token, "remote", "set-head", "origin", "--auto")
	return err
}

// DefaultBranch returns the branch origin/HEAD points to.
func (r *Repo) DefaultBranch(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/"), nil
}

// RevParse resolves ref to a commit SHA.
func (r *Repo) RevParse(ctx context.Context, ref string) (string, error) {
	out, err := r.git(ctx, "", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Show returns the content of path at ref, or ErrNotFound if ref exists
// but doesn't contain path.
func (r *Repo) Show(ctx context.Context, ref, path string) ([]byte, error) {
	out, err := r.git(ctx, "", "cat-file", "blob", ref+":"+path)
	if err == nil {
		return out, nil
	}
	if _, rerr := r.RevParse(ctx, ref); rerr != nil {
		return nil, fmt.Errorf("unknown ref %s: %w", ref, rerr)
	}
	return nil, fmt.Errorf("%s:%s: %w", ref, path, ErrNotFound)
}

// OriginURL returns origin's URL.
func (r *Repo) OriginURL(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "", "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

var scpLike = regexp.MustCompile(`^(?:[\w.-]+@)?github\.com:([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

// GitHubRepo is a repository on github.com.
type GitHubRepo struct{ Owner, Name string }

// URL is the HTTPS clone URL; App tokens only work over HTTPS.
func (g GitHubRepo) URL() string { return "https://github.com/" + g.Owner + "/" + g.Name + ".git" }

func (g GitHubRepo) String() string { return g.Owner + "/" + g.Name }

// ParseGitHubURL accepts https://github.com/o/r(.git), git@github.com:o/r(.git),
// ssh://git@github.com/o/r and o/r.
func ParseGitHubURL(raw string) (GitHubRepo, error) {
	raw = strings.TrimSpace(raw)
	if m := scpLike.FindStringSubmatch(raw); m != nil {
		return GitHubRepo{m[1], m[2]}, nil
	}
	if !strings.Contains(raw, "://") && strings.Count(raw, "/") == 1 {
		raw = "https://github.com/" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "ssh") || !strings.EqualFold(u.Hostname(), "github.com") {
		return GitHubRepo{}, fmt.Errorf("%q is not a github.com repository URL", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return GitHubRepo{}, fmt.Errorf("%q is not a github.com repository URL", raw)
	}
	return GitHubRepo{parts[0], strings.TrimSuffix(parts[1], ".git")}, nil
}
