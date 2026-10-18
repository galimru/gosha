package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrWorkflowPermission means GitHub refused a push because it changes
// .github/workflows/ and the App has no Workflows permission (DESIGN §8.6).
var ErrWorkflowPermission = errors.New("the push changes .github/workflows/, which the Gosha GitHub App is not allowed to do")

// ErrNotCheckout means a ticket checkout's .git isn't a plain directory.
var ErrNotCheckout = errors.New("not a ticket checkout")

const workflowRejection = "refusing to allow a GitHub App to create or update workflow"

// refExists reports whether ref exists in the repo.
func (r *Repo) refExists(ctx context.Context, ref string) (bool, error) {
	_, err := r.git(ctx, "", "show-ref", "--verify", "--quiet", ref)
	var ee *exec.ExitError
	if err != nil && errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// RemoteBranchExists reports whether origin has branch, as of the last fetch.
func (r *Repo) RemoteBranchExists(ctx context.Context, branch string) (bool, error) {
	return r.refExists(ctx, "refs/remotes/origin/"+branch)
}

// LocalBranchExists reports whether the bare clone has refs/heads/<branch>,
// i.e. Gosha has collected work for it from a checkout.
func (r *Repo) LocalBranchExists(ctx context.Context, branch string) (bool, error) {
	return r.refExists(ctx, "refs/heads/"+branch)
}

// CloneCheckout makes dir a self-contained checkout of branch for an agent.
// It isn't a linked worktree: the agent can write to everything in dir, so
// nothing in it may be shared with the bare clone (DESIGN §8.2). The branch
// starts from the bare clone's refs/heads/<branch> if Gosha already has work
// on it, otherwise from origin/<base>. An existing dir is reused as it is.
func (r *Repo) CloneCheckout(ctx context.Context, dir, base, branch string) (fresh bool, err error) {
	if _, err := os.Stat(dir); err == nil {
		return false, nil
	}
	from := "refs/remotes/origin/" + base
	if ok, err := r.LocalBranchExists(ctx, branch); err != nil {
		return false, err
	} else if ok {
		from = "refs/heads/" + branch
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return false, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".checkout-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	steps := [][]string{
		{"init", "--quiet", "--initial-branch=" + branch},
		{"fetch", "--quiet", "--no-tags", r.Dir,
			"+refs/remotes/origin/" + base + ":refs/remotes/origin/" + base,
			"+" + from + ":refs/gosha/start"},
		{"reset", "--quiet", "--hard", "refs/gosha/start"},
		{"update-ref", "-d", "refs/gosha/start"},
	}
	for _, args := range steps {
		if _, err := run(ctx, tmp, "", args...); err != nil {
			return false, err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		return false, err
	}
	return true, nil
}

// FetchFrom collects branch from an agent's checkout into the bare clone's
// refs/heads/<branch>. Git runs upload-pack in the checkout, which ignores
// that repo's hook-type config; Gosha never runs git inside it otherwise.
func (r *Repo) FetchFrom(ctx context.Context, dir, branch string) error {
	gitDir := filepath.Join(dir, ".git")
	fi, err := os.Lstat(gitDir)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("%s: %w", dir, ErrNotCheckout)
	}
	_, err = r.git(ctx, "", "fetch", "--quiet", "--no-tags", gitDir,
		"+refs/heads/"+branch+":refs/heads/"+branch)
	return err
}

// CommitsAhead counts the commits on branch that aren't on origin/<base>.
func (r *Repo) CommitsAhead(ctx context.Context, base, branch string) (int, error) {
	out, err := r.git(ctx, "", "rev-list", "--count", "refs/remotes/origin/"+base+"..refs/heads/"+branch)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// DiffStat is the size of a change.
type DiffStat struct{ Files, Added, Deleted int }

func (d DiffStat) String() string {
	return fmt.Sprintf("+%d −%d in %d files", d.Added, d.Deleted, d.Files)
}

// Diff measures branch against its merge base with origin/<base>.
func (r *Repo) Diff(ctx context.Context, base, branch string) (DiffStat, error) {
	out, err := r.git(ctx, "", "diff", "--numstat", "refs/remotes/origin/"+base+"...refs/heads/"+branch)
	if err != nil {
		return DiffStat{}, err
	}
	var d DiffStat
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) < 3 {
			continue
		}
		d.Files++
		// Binary files show "-".
		a, _ := strconv.Atoi(f[0])
		x, _ := strconv.Atoi(f[1])
		d.Added += a
		d.Deleted += x
	}
	return d, nil
}

// DiffText is the diff of branch against its merge base with origin/<base>.
func (r *Repo) DiffText(ctx context.Context, base, branch string) (string, error) {
	out, err := r.git(ctx, "", "diff", "refs/remotes/origin/"+base+"...refs/heads/"+branch)
	return string(out), err
}

// Push pushes the bare clone's branch to origin. It never forces: a branch
// that moved on GitHub is an error, not something to overwrite.
func (r *Repo) Push(ctx context.Context, token, branch string) error {
	_, err := r.git(ctx, token, "push", "--quiet", "origin", "refs/heads/"+branch+":refs/heads/"+branch)
	var gerr *Error
	if errors.As(err, &gerr) && strings.Contains(gerr.Stderr, workflowRejection) {
		return fmt.Errorf("%w: %s", ErrWorkflowPermission, strings.TrimSpace(gerr.Stderr))
	}
	if err != nil {
		return err
	}
	// Record the pushed state as origin's, as a fetch would.
	_, err = r.git(ctx, "", "update-ref", "refs/remotes/origin/"+branch, "refs/heads/"+branch)
	return err
}

// DeleteBranch deletes branch on origin (if it's there) and in the bare clone.
func (r *Repo) DeleteBranch(ctx context.Context, token, branch string) error {
	if ok, err := r.RemoteBranchExists(ctx, branch); err != nil {
		return err
	} else if ok {
		_, err := r.git(ctx, token, "push", "--quiet", "origin", "--delete", "refs/heads/"+branch)
		var gerr *Error
		if err != nil && !(errors.As(err, &gerr) && strings.Contains(gerr.Stderr, "remote ref does not exist")) {
			return err
		}
		if _, err := r.git(ctx, "", "update-ref", "-d", "refs/remotes/origin/"+branch); err != nil {
			return err
		}
	}
	if ok, err := r.LocalBranchExists(ctx, branch); err != nil || !ok {
		return err
	}
	_, err := r.git(ctx, "", "update-ref", "-d", "refs/heads/"+branch)
	return err
}
