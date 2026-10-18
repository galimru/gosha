package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/gitx"
)

func TestPatchLines(t *testing.T) {
	patch := "@@ -1,3 +1,4 @@\n package x\n-old\n+new\n+added\n ctx\n@@ -20,2 +21,2 @@ func f() {\n-a\n+b\n\\ No newline at end of file"
	got := PatchLines(patch)
	for _, n := range []int{1, 2, 3, 4, 21} {
		if !got[n] {
			t.Errorf("line %d not commentable: %v", n, got)
		}
	}
	if got[5] || got[20] || len(got) != 5 {
		t.Errorf("lines = %v", got)
	}
}

// installation serves the token endpoints plus handle for everything else.
func installation(t *testing.T, handle func(w http.ResponseWriter, r *http.Request) bool) *Installation {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/o/r/installation":
			w.Write([]byte(`{"id": 7}`))
		case "POST /app/installations/7/access_tokens":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"token": "ghs_test", "expires_at": "` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`))
		default:
			if !handle(w, r) {
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}
	}))
	t.Cleanup(srv.Close)
	app, err := NewApp(42, writeKey(t), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := app.Installation(context.Background(), gitx.GitHubRepo{Owner: "o", Name: "r"})
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func TestCreateReview(t *testing.T) {
	var got struct {
		CommitID string `json:"commit_id"`
		Body     string
		Event    string
		Comments []struct {
			Path, Side, Body string
			Line             int
		}
	}
	inst := installation(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/o/r/pulls/5/files":
			w.Write([]byte(`[{"filename": "a.go", "patch": "@@ -1,1 +1,2 @@\n x\n+y"}]`))
		case "POST /repos/o/r/pulls/5/reviews":
			json.NewDecoder(r.Body).Decode(&got)
			w.Write([]byte(`{"id": 99}`))
		default:
			return false
		}
		return true
	})
	id, err := inst.CreateReview(context.Background(), 5, "abc", "❌ changes requested", []ReviewComment{
		{Path: "a.go", Line: 2, Body: "inline"},
		{Path: "a.go", Line: 40, Body: "outside the diff"},
		{Path: "b.go", Body: "whole file"},
	})
	if err != nil || id != 99 {
		t.Fatalf("CreateReview = %d, %v", id, err)
	}
	if got.CommitID != "abc" || got.Event != "COMMENT" || len(got.Comments) != 1 ||
		got.Comments[0].Path != "a.go" || got.Comments[0].Line != 2 || got.Comments[0].Side != "RIGHT" {
		t.Errorf("review = %+v", got)
	}
	for _, want := range []string{"❌ changes requested", "**Other comments**", "`a.go:40`: outside the diff", "`b.go`: whole file"} {
		if !strings.Contains(got.Body, want) {
			t.Errorf("body missing %q:\n%s", want, got.Body)
		}
	}
}

func TestHumanFeedback(t *testing.T) {
	since := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	before, after, later := "2026-10-18T09:00:00Z", "2026-10-18T11:00:00Z", "2026-10-18T12:00:00Z"
	human, bot := `{"login": "ann", "type": "User"}`, `{"login": "goshabot[bot]", "type": "Bot"}`
	inst := installation(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/o/r/issues/5/comments":
			if r.URL.Query().Get("since") == "" {
				t.Error("no since on issue comments")
			}
			w.Write([]byte(`[
				{"user": ` + human + `, "body": "old", "created_at": "` + before + `"},
				{"user": ` + bot + `, "body": "round 1 addressed", "created_at": "` + after + `"},
				{"user": ` + human + `, "body": "please rename", "created_at": "` + later + `"}]`))
		case "GET /repos/o/r/pulls/5/comments":
			w.Write([]byte(`[{"user": ` + human + `, "body": "off by one", "path": "a.go", "line": 7, "created_at": "` + after + `"}]`))
		case "GET /repos/o/r/pulls/5/reviews":
			w.Write([]byte(`[
				{"user": ` + bot + `, "body": "✅", "state": "COMMENTED", "submitted_at": "` + after + `"},
				{"user": ` + human + `, "body": "", "state": "APPROVED", "submitted_at": "` + after + `"},
				{"user": ` + human + `, "body": "draft", "state": "PENDING"},
				{"user": ` + human + `, "body": "needs tests", "state": "CHANGES_REQUESTED", "submitted_at": "` + after + `"}]`))
		default:
			return false
		}
		return true
	})
	fb, err := inst.HumanFeedback(context.Background(), 5, since)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fb {
		got = append(got, f.Kind+":"+f.Body)
	}
	want := "inline comment:off by one|review:needs tests|comment:please rename"
	if strings.Join(got, "|") != want {
		t.Errorf("feedback = %v, want %s", got, want)
	}
	if fb[0].Author != "ann" || fb[0].Path != "a.go" || fb[0].Line != 7 {
		t.Errorf("inline = %+v", fb[0])
	}
}
