package gitx

import (
	"context"
	"errors"
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

// TakeOrigin points the bare clone's branch at origin's, after GitHub
// changed it (merging the base in, DESIGN §8.5) and it was fetched.
func (r *Repo) TakeOrigin(ctx context.Context, branch string) error {
	_, err := r.git(ctx, "", "update-ref", "refs/heads/"+branch, "refs/remotes/origin/"+branch)
	return err
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
