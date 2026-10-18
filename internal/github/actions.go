package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v88/github"
)

// WorkflowRun is one Actions workflow run. A re-run keeps its ID, and
// Status and Conclusion are those of its latest attempt.
type WorkflowRun struct {
	ID         int64
	Status     string // queued, in_progress, completed, …
	Conclusion string // once completed: success, failure, cancelled, …
	URL        string
}

// Job is one job of a workflow run.
type Job struct {
	ID         int64
	Name       string
	Conclusion string
	URL        string
}

// DeployRun returns the newest run of the workflow file (e.g. deploy.yml)
// for commit sha on branch, or nil when there's none yet.
func (i *Installation) DeployRun(ctx context.Context, workflow, sha, branch string) (*WorkflowRun, error) {
	res, _, err := i.api.Actions.ListWorkflowRunsByFileName(ctx, i.Repo.Owner, i.Repo.Name, workflow, &gh.ListWorkflowRunsOptions{
		Branch: branch, HeadSHA: sha, ListOptions: gh.ListOptions{PerPage: 1},
	})
	if err != nil {
		return nil, fmt.Errorf("github runs of %s for %.7s: %w", workflow, sha, err)
	}
	if len(res.WorkflowRuns) == 0 {
		return nil, nil
	}
	r := res.WorkflowRuns[0]
	return &WorkflowRun{ID: r.GetID(), Status: r.GetStatus(), Conclusion: r.GetConclusion(), URL: r.GetHTMLURL()}, nil
}

// RunJobs lists the jobs of a workflow run's latest attempt.
func (i *Installation) RunJobs(ctx context.Context, runID int64) ([]Job, error) {
	var out []Job
	opts := &gh.ListWorkflowJobsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		res, resp, err := i.api.Actions.ListWorkflowJobs(ctx, i.Repo.Owner, i.Repo.Name, runID, opts)
		if err != nil {
			return nil, fmt.Errorf("github jobs of run %d: %w", runID, err)
		}
		for _, j := range res.Jobs {
			out = append(out, Job{ID: j.GetID(), Name: j.GetName(), Conclusion: j.GetConclusion(), URL: j.GetHTMLURL()})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}
