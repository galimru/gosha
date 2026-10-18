package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/gitx"
)

func TestPullRequests(t *testing.T) {
	var created, closed atomic.Int32
	open := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/repos/o/r/installation" && r.URL.Path != "/app/installations/7/access_tokens" &&
			r.Header.Get("Authorization") != "token ghs_test" {
			t.Errorf("%s %s without the installation token", r.Method, r.URL.Path)
		}
		pr := `{"number": 5, "html_url": "https://github.com/o/r/pull/5", "state": "open", "head": {"sha": "abc"}}`
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/o/r/installation":
			w.Write([]byte(`{"id": 7}`))
		case "POST /app/installations/7/access_tokens":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"token": "ghs_test", "expires_at": "` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`))
		case "GET /users/goshabot[bot]":
			w.Write([]byte(`{"id": 340970700, "login": "goshabot[bot]"}`))
		case "GET /repos/o/r/pulls":
			if q := r.URL.Query(); q.Get("head") != "o:gosha/NXT-1" || q.Get("state") != "open" {
				t.Errorf("list query %v", q)
			}
			if open {
				w.Write([]byte("[" + pr + "]"))
			} else {
				w.Write([]byte(`[]`))
			}
		case "POST /repos/o/r/pulls":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["head"] != "gosha/NXT-1" || body["base"] != "main" || body["title"] != "NXT-1: X" {
				t.Errorf("create body %v", body)
			}
			created.Add(1)
			open = true
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(pr))
		case "GET /repos/o/r/pulls/5":
			w.Write([]byte(`{"number": 5, "state": "closed", "merged": true, "head": {"sha": "abc"}}`))
		case "PATCH /repos/o/r/pulls/5":
			closed.Add(1)
			w.Write([]byte(pr))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	app, err := NewApp(42, writeKey(t), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := app.Installation(ctx, gitx.GitHubRepo{Owner: "o", Name: "r"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := inst.BotIdentity(ctx, AppInfo{Slug: "goshabot"})
	if err != nil || id != (Identity{"goshabot[bot]", "340970700+goshabot[bot]@users.noreply.github.com"}) {
		t.Errorf("BotIdentity = %+v, %v", id, err)
	}
	for range 2 {
		p, err := inst.CreatePR(ctx, "gosha/NXT-1", "main", "NXT-1: X", "body")
		if err != nil || p.Number != 5 || p.URL != "https://github.com/o/r/pull/5" {
			t.Fatalf("CreatePR = %+v, %v", p, err)
		}
	}
	if created.Load() != 1 {
		t.Errorf("created %d PRs, want 1 (idempotent)", created.Load())
	}
	if p, err := inst.PR(ctx, 5); err != nil || !p.Merged || p.State != "closed" {
		t.Errorf("PR = %+v, %v", p, err)
	}
	if err := inst.ClosePR(ctx, 5); err != nil || closed.Load() != 1 {
		t.Errorf("ClosePR: %v", err)
	}
}
