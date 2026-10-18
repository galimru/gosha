package github

import (
	"context"
	"net/http"
	"testing"
)

func TestDeployRun(t *testing.T) {
	inst := installation(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method+" "+r.URL.Path != "GET /repos/o/r/actions/workflows/deploy.yml/runs" {
			return false
		}
		q := r.URL.Query()
		if q.Get("head_sha") == "none" {
			w.Write([]byte(`{"total_count": 0, "workflow_runs": []}`))
			return true
		}
		if q.Get("head_sha") != "abc" || q.Get("branch") != "main" {
			t.Errorf("query = %v", q)
		}
		w.Write([]byte(`{"total_count": 1, "workflow_runs": [
			{"id": 7, "status": "completed", "conclusion": "failure", "html_url": "u7"}]}`))
		return true
	})
	ctx := context.Background()
	got, err := inst.DeployRun(ctx, "deploy.yml", "abc", "main")
	if err != nil {
		t.Fatal(err)
	}
	if want := (WorkflowRun{ID: 7, Status: "completed", Conclusion: "failure", URL: "u7"}); got == nil || *got != want {
		t.Errorf("DeployRun = %+v, want %+v", got, want)
	}
	if got, err := inst.DeployRun(ctx, "deploy.yml", "none", "main"); err != nil || got != nil {
		t.Errorf("DeployRun with no run = %+v, %v", got, err)
	}
}

func TestRunJobs(t *testing.T) {
	inst := installation(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method+" "+r.URL.Path != "GET /repos/o/r/actions/runs/7/jobs" {
			return false
		}
		w.Write([]byte(`{"total_count": 2, "jobs": [
			{"id": 1, "name": "build", "conclusion": "success", "html_url": "j1"},
			{"id": 2, "name": "ship", "conclusion": "failure", "html_url": "j2"}]}`))
		return true
	})
	jobs, err := inst.RunJobs(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[1] != (Job{ID: 2, Name: "ship", Conclusion: "failure", URL: "j2"}) {
		t.Errorf("RunJobs = %+v", jobs)
	}
}
