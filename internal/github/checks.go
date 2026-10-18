package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	gh "github.com/google/go-github/v88/github"
)

// Check kinds.
const (
	KindCheckRun = "check run"
	KindStatus   = "status"
)

// Check is one check run or commit status on a commit, normalised: Status
// is queued, in_progress or completed, and Conclusion (once completed) is
// GitHub's check-run conclusion, with statuses mapped onto it.
type Check struct {
	Name       string
	Kind       string
	Status     string
	Conclusion string
	URL        string
	// Summary is a status's description or a check run's output title.
	Summary string
	// JobID is the Actions job behind a check run (its id), 0 otherwise.
	JobID int64
}

// Passed reports a completed check that doesn't block a merge.
func (c Check) Passed() bool {
	switch c.Conclusion {
	case "success", "neutral", "skipped":
		return true
	}
	return false
}

// Checks lists the check runs and commit statuses on sha. For statuses,
// only the newest per context counts, as on GitHub's own PR page.
func (i *Installation) Checks(ctx context.Context, sha string) ([]Check, error) {
	var out []Check
	opts := &gh.ListCheckRunsOptions{Filter: gh.Ptr("latest"), ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		res, resp, err := i.api.Checks.ListCheckRunsForRef(ctx, i.Repo.Owner, i.Repo.Name, sha, opts)
		if err != nil {
			return nil, fmt.Errorf("github check runs for %.7s: %w", sha, err)
		}
		for _, r := range res.CheckRuns {
			c := Check{Name: r.GetName(), Kind: KindCheckRun, Status: r.GetStatus(), Conclusion: r.GetConclusion(),
				URL: r.GetHTMLURL(), Summary: r.GetOutput().GetTitle()}
			if r.GetApp().GetSlug() == "github-actions" {
				c.JobID = r.GetID()
			}
			out = append(out, c)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	seen := map[string]bool{}
	lo := &gh.ListOptions{PerPage: 100}
	for {
		sts, resp, err := i.api.Repositories.ListStatuses(ctx, i.Repo.Owner, i.Repo.Name, sha, lo)
		if err != nil {
			return nil, fmt.Errorf("github statuses for %.7s: %w", sha, err)
		}
		// Newest first, so the first of each context is the current one.
		for _, s := range sts {
			if seen[s.GetContext()] {
				continue
			}
			seen[s.GetContext()] = true
			c := Check{Name: s.GetContext(), Kind: KindStatus, Status: "completed", URL: s.GetTargetURL(), Summary: s.GetDescription()}
			switch s.GetState() {
			case "pending":
				c.Status = "in_progress"
			case "success":
				c.Conclusion = "success"
			default: // failure, error
				c.Conclusion = "failure"
			}
			out = append(out, c)
		}
		if resp.NextPage == 0 {
			break
		}
		lo.Page = resp.NextPage
	}
	return out, nil
}

// maxJobLog is how much of a job log JobLog keeps: its end, where the
// failure is.
const maxJobLog = 2 << 20

// JobLog downloads an Actions job's log, keeping its last 2 MiB.
func (i *Installation) JobLog(ctx context.Context, jobID int64) (string, error) {
	u, _, err := i.api.Actions.GetWorkflowJobLogs(ctx, i.Repo.Owner, i.Repo.Name, jobID, 1)
	if err != nil {
		return "", fmt.Errorf("github job %d log: %w", jobID, err)
	}
	// The log is behind a short-lived signed URL that needs no token.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github job %d log: %w", jobID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github job %d log: %s", jobID, resp.Status)
	}
	var tail tailBuffer
	if _, err := io.Copy(&tail, resp.Body); err != nil {
		return "", fmt.Errorf("github job %d log: %w", jobID, err)
	}
	return string(tail.buf), nil
}

type tailBuffer struct{ buf []byte }

func (w *tailBuffer) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - maxJobLog; over > 0 {
		w.buf = w.buf[over:]
	}
	return len(p), nil
}

// ErrHeadMoved means the PR's head is no longer the SHA a merge was pinned
// to: something was pushed after the checks ran.
var ErrHeadMoved = errors.New("the pull request's head moved since it was checked")

// ErrNotMergeable means GitHub refused the merge, e.g. on a conflict.
var ErrNotMergeable = errors.New("the pull request is not mergeable")

// Merge merges a pull request with method (squash, merge or rebase), only
// if its head is still sha (DESIGN §8.5). It returns the merge commit.
func (i *Installation) Merge(ctx context.Context, number int, sha, method, title, message string) (string, error) {
	res, resp, err := i.api.PullRequests.Merge(ctx, i.Repo.Owner, i.Repo.Name, number, message, &gh.PullRequestOptions{
		CommitTitle: title, SHA: sha, MergeMethod: method, DontDefaultIfBlank: true,
	})
	if resp != nil {
		switch resp.StatusCode {
		case http.StatusConflict:
			return "", fmt.Errorf("github merge PR #%d: %w: %v", number, ErrHeadMoved, err)
		case http.StatusMethodNotAllowed:
			return "", fmt.Errorf("github merge PR #%d: %w: %v", number, ErrNotMergeable, err)
		}
	}
	if err != nil {
		return "", fmt.Errorf("github merge PR #%d: %w", number, err)
	}
	return res.GetSHA(), nil
}
