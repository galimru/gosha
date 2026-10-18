package gitx

import (
	"context"
	"errors"
	"os/exec"
)

// IsAncestor reports whether commit a is an ancestor of (or equal to) b.
func (r *Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	_, err := r.git(ctx, "", "merge-base", "--is-ancestor", a, b)
	var ee *exec.ExitError
	if err != nil && errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// TakeOrigin points the bare clone's branch at origin's, after GitHub
// changed it (merging the base in, DESIGN §8.5) and it was fetched.
func (r *Repo) TakeOrigin(ctx context.Context, branch string) error {
	_, err := r.git(ctx, "", "update-ref", "refs/heads/"+branch, "refs/remotes/origin/"+branch)
	return err
}
