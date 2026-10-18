package gitx

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"
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

// Author is who a commit Gosha makes is authored and committed by.
type Author struct{ Name, Email string }

// MergeBase merges origin/<base> into the bare clone's branch without a
// worktree (git merge-tree, git ≥ 2.38), so Gosha never runs git in an
// agent's checkout (DESIGN §8.5). On conflicts the branch is left as it is
// and the conflicted paths are returned.
func (r *Repo) MergeBase(ctx context.Context, base, branch string, by Author) (conflicts []string, err error) {
	theirs, ours := "refs/remotes/origin/"+base, "refs/heads/"+branch
	// merge-tree exits 1 on a conflict, with the conflicted paths after the
	// tree on stdout.
	cmd := exec.CommandContext(ctx, "git", "merge-tree", "--write-tree", "--name-only", "--no-messages", ours, theirs)
	cmd.Dir = r.Dir
	cmd.Env = Env("")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var ee *exec.ExitError
	if err != nil && errors.As(err, &ee) && ee.ExitCode() == 1 {
		if len(lines) < 2 {
			return nil, fmt.Errorf("merge %s into %s: conflict without paths", base, branch)
		}
		return lines[1:], nil
	}
	if err != nil {
		return nil, &Error{Args: cmd.Args[1:], Stderr: stderr.String(), Err: err}
	}
	tree := lines[0]
	cmd = exec.CommandContext(ctx, "git", "commit-tree", tree, "-p", ours, "-p", theirs,
		"-m", fmt.Sprintf("Merge %s into %s", base, branch))
	cmd.Dir = r.Dir
	cmd.Env = append(Env(""),
		"GIT_AUTHOR_NAME="+by.Name, "GIT_AUTHOR_EMAIL="+by.Email,
		"GIT_COMMITTER_NAME="+by.Name, "GIT_COMMITTER_EMAIL="+by.Email)
	cmd.Stderr = nil
	commit, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git commit-tree: %w", err)
	}
	_, err = r.git(ctx, "", "update-ref", ours, strings.TrimSpace(string(commit)))
	return nil, err
}

// HasCIWorkflows reports whether .github/workflows/ at ref has a workflow
// that runs on pull_request, pull_request_target or push, i.e. a PR is
// expected to get at least one check (DESIGN §8.4).
func (r *Repo) HasCIWorkflows(ctx context.Context, ref string) (bool, error) {
	out, err := r.git(ctx, "", "ls-tree", "--name-only", ref, ".github/workflows/")
	if err != nil {
		return false, err
	}
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ext := path.Ext(f); ext != ".yml" && ext != ".yaml" {
			continue
		}
		data, err := r.Show(ctx, ref, f)
		if err != nil {
			return false, err
		}
		if RunsOnPR(data) {
			return true, nil
		}
	}
	return false, nil
}

// RunsOnPR reports whether a workflow file triggers on pull_request,
// pull_request_target or push. `on` can be a string, a list or a map. A
// file that doesn't parse counts as one that runs: GitHub reports a broken
// workflow as a failed check.
func RunsOnPR(data []byte) bool {
	var wf map[string]any
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return true
	}
	var events []string
	switch on := wf["on"].(type) {
	case string:
		events = []string{on}
	case []any:
		for _, e := range on {
			if s, ok := e.(string); ok {
				events = append(events, s)
			}
		}
	case map[string]any:
		for e := range on {
			events = append(events, e)
		}
	}
	for _, e := range events {
		switch e {
		case "pull_request", "pull_request_target", "push":
			return true
		}
	}
	return false
}

// MinVersion is the oldest git Gosha works with: MergeBase needs
// `merge-tree --write-tree`.
var MinVersion = [2]int{2, 38}

// Version returns the installed git's major and minor version.
func Version(ctx context.Context) (major, minor int, err error) {
	out, err := run(ctx, "", "", "--version")
	if err != nil {
		return 0, 0, err
	}
	// "git version 2.54.0 (Apple Git-157)"
	f := strings.Fields(string(out))
	if len(f) < 3 {
		return 0, 0, fmt.Errorf("unexpected git --version output %q", out)
	}
	if _, err := fmt.Sscanf(f[2], "%d.%d", &major, &minor); err != nil {
		return 0, 0, fmt.Errorf("unexpected git --version output %q", out)
	}
	return major, minor, nil
}
