package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestChecks(t *testing.T) {
	inst := installation(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/o/r/commits/abc/check-runs":
			w.Write([]byte(`{"total_count": 2, "check_runs": [
				{"id": 11, "name": "test", "status": "completed", "conclusion": "failure", "html_url": "u1",
				 "output": {"title": "1 failed"}, "app": {"slug": "github-actions"}},
				{"id": 12, "name": "lint", "status": "in_progress", "app": {"slug": "other"}}]}`))
		case "GET /repos/o/r/commits/abc/statuses":
			w.Write([]byte(`[
				{"context": "ci/x", "state": "success", "target_url": "u2"},
				{"context": "ci/x", "state": "failure"},
				{"context": "ci/y", "state": "pending", "description": "waiting"}]`))
		default:
			return false
		}
		return true
	})
	cs, err := inst.Checks(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	want := []Check{
		{Name: "test", Kind: KindCheckRun, Status: "completed", Conclusion: "failure", URL: "u1", Summary: "1 failed", JobID: 11},
		{Name: "lint", Kind: KindCheckRun, Status: "in_progress"},
		{Name: "ci/x", Kind: KindStatus, Status: "completed", Conclusion: "success", URL: "u2"},
		{Name: "ci/y", Kind: KindStatus, Status: "in_progress", Summary: "waiting"},
	}
	if len(cs) != len(want) {
		t.Fatalf("Checks = %+v", cs)
	}
	for i := range want {
		if cs[i] != want[i] {
			t.Errorf("check %d = %+v, want %+v", i, cs[i], want[i])
		}
	}
	if cs[0].Passed() || !cs[2].Passed() {
		t.Error("Passed")
	}
}

func TestJobLog(t *testing.T) {
	var base string
	inst := installation(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/o/r/actions/jobs/11/logs":
			http.Redirect(w, r, base+"/blob/11", http.StatusFound)
		case "GET /blob/11":
			if r.Header.Get("Authorization") != "" {
				t.Error("the signed log URL got the installation token")
			}
			w.Write([]byte(strings.Repeat("x", maxJobLog) + "the end"))
		default:
			return false
		}
		return true
	})
	base = strings.TrimSuffix(inst.api.BaseURL(), "/")
	log, err := inst.JobLog(context.Background(), 11)
	if err != nil || len(log) != maxJobLog || !strings.HasSuffix(log, "the end") {
		t.Fatalf("JobLog = %d bytes, %v", len(log), err)
	}
}

func TestMerge(t *testing.T) {
	status := http.StatusOK
	inst := installation(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method+" "+r.URL.Path != "PUT /repos/o/r/pulls/5/merge" {
			return false
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["sha"] != "abc" || body["merge_method"] != "squash" || body["commit_title"] != "NXT-1: X (#5)" || body["commit_message"] != "msg" {
			t.Errorf("merge body %v", body)
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Write([]byte(`{"sha": "def", "merged": true}`))
		} else {
			w.Write([]byte(`{"message": "nope"}`))
		}
		return true
	})
	ctx := context.Background()
	if sha, err := inst.Merge(ctx, 5, "abc", "squash", "NXT-1: X (#5)", "msg"); err != nil || sha != "def" {
		t.Fatalf("Merge = %q, %v", sha, err)
	}
	status = http.StatusConflict
	if _, err := inst.Merge(ctx, 5, "abc", "squash", "NXT-1: X (#5)", "msg"); !errors.Is(err, ErrHeadMoved) {
		t.Errorf("409: %v", err)
	}
	status = http.StatusMethodNotAllowed
	if _, err := inst.Merge(ctx, 5, "abc", "squash", "NXT-1: X (#5)", "msg"); !errors.Is(err, ErrNotMergeable) {
		t.Errorf("405: %v", err)
	}
}
