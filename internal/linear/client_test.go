package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/config"
)

type fake struct {
	t        *testing.T
	mints    atomic.Int32
	calls    atomic.Int32
	validTok atomic.Value // string
	handle   func(query string, vars map[string]any) (status int, body string)
}

func newFake(t *testing.T, handle func(string, map[string]any) (int, string)) (*fake, *Client) {
	f := &fake{t: t, handle: handle}
	f.validTok.Store("tok-1")
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "id" || r.Form.Get("client_secret") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		n := f.mints.Add(1)
		fmt.Fprintf(w, `{"access_token":"tok-%d","token_type":"Bearer","expires_in":2592000,"scope":%q}`, n, r.Form.Get("scope"))
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+f.validTok.Load().(string) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"errors":[{"message":"Authentication required","extensions":{"code":"AUTHENTICATION_ERROR"}}]}`))
			return
		}
		var req struct {
			Query     string
			Variables map[string]any
		}
		json.NewDecoder(r.Body).Decode(&req)
		status, body := f.handle(req.Query, req.Variables)
		w.WriteHeader(status)
		w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, New("id", "secret", WithURLs(srv.URL+"/graphql", srv.URL+"/oauth/token"), WithBackoff(time.Millisecond))
}

func TestTokenCachedAndRenewedOnAuthError(t *testing.T) {
	f, c := newFake(t, func(string, map[string]any) (int, string) {
		return 200, `{"data":{"viewer":{"id":"u1","name":"GoshaBot","displayName":"gosha"}}}`
	})
	ctx := context.Background()
	for range 2 {
		v, err := c.Viewer(ctx)
		if err != nil || v.Name != "GoshaBot" {
			t.Fatalf("Viewer = %+v, %v", v, err)
		}
	}
	if f.mints.Load() != 1 {
		t.Fatalf("minted %d tokens, want 1", f.mints.Load())
	}
	// The server stops accepting tok-1: the client re-mints once and retries.
	f.validTok.Store("tok-2")
	if _, err := c.Viewer(ctx); err != nil {
		t.Fatal(err)
	}
	if f.mints.Load() != 2 {
		t.Errorf("minted %d tokens, want 2", f.mints.Load())
	}
	// A token the server never accepts fails after one renewal, not forever.
	f.validTok.Store("never")
	if _, err := c.Viewer(ctx); err == nil {
		t.Error("expected an auth error")
	}
	if f.mints.Load() != 3 {
		t.Errorf("minted %d tokens, want 3", f.mints.Load())
	}
}

func TestBadCredentials(t *testing.T) {
	_, c := newFake(t, nil)
	c.clientSecret = "wrong"
	if _, err := c.Viewer(context.Background()); err == nil || !strings.Contains(err.Error(), "linear token") {
		t.Errorf("expected a token error, got %v", err)
	}
}

func TestRateLimitRetry(t *testing.T) {
	var n atomic.Int32
	f, c := newFake(t, func(string, map[string]any) (int, string) {
		if n.Add(1) <= 2 {
			return 400, `{"errors":[{"message":"Rate limit exceeded","extensions":{"code":"RATELIMITED"}}]}`
		}
		return 200, `{"data":{"viewer":{"id":"u1","name":"GoshaBot"}}}`
	})
	if _, err := c.Viewer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", f.calls.Load())
	}

	n.Store(-100) // always limited
	if _, err := c.Viewer(context.Background()); err == nil {
		t.Error("expected the rate limit to surface after retries")
	}
}

func TestTeamAndStates(t *testing.T) {
	_, c := newFake(t, func(q string, v map[string]any) (int, string) {
		if v["key"] != "NXT" {
			return 200, `{"data":{"teams":{"nodes":[]}}}`
		}
		return 200, `{"data":{"teams":{"nodes":[{"id":"t1","key":"NXT","name":"NextDesk","states":{"nodes":[
			{"id":"s1","name":"Ready","type":"unstarted"},
			{"id":"s2","name":"In Progress","type":"started"},
			{"id":"s3","name":"In Review","type":"started"},
			{"id":"s4","name":"Blocked","type":"started"},
			{"id":"s5","name":"Shipped","type":"completed"}]}}]}}}`
	})
	ctx := context.Background()
	team, err := c.Team(ctx, "NXT")
	if err != nil {
		t.Fatal(err)
	}
	st, err := ResolveStates(team, config.States{Ready: "Ready", InProgress: "In Progress", InReview: "In Review", Blocked: "Blocked"})
	if err != nil || st.Ready.ID != "s1" || st.Blocked.ID != "s4" {
		t.Fatalf("ResolveStates = %+v, %v", st, err)
	}
	_, err = ResolveStates(team, config.States{Ready: "Queued", InProgress: "In Progress", InReview: "QA", Blocked: "Blocked"})
	if err == nil || !strings.Contains(err.Error(), `ready ("Queued")`) || !strings.Contains(err.Error(), `in_review ("QA")`) {
		t.Errorf("missing states error = %v", err)
	}
	if _, err := c.Team(ctx, "NOPE"); err == nil {
		t.Error("expected team not found")
	}
}

func TestIssuesPaginationAndBlockers(t *testing.T) {
	_, c := newFake(t, func(q string, v map[string]any) (int, string) {
		if v["after"] == nil {
			return 200, `{"data":{"issues":{"nodes":[
				{"id":"i1","identifier":"NXT-1","number":1,"title":"One","url":"u1","priority":2,"createdAt":"2026-10-01T00:00:00Z",
				 "state":{"id":"s1","name":"Ready","type":"unstarted"},
				 "inverseRelations":{"nodes":[
					{"type":"blocks","issue":{"identifier":"NXT-9","state":{"id":"s2","name":"In Progress","type":"started"}}},
					{"type":"related","issue":{"identifier":"NXT-8","state":{"id":"s2","name":"In Progress","type":"started"}}}]}}],
				"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}}}`
		}
		return 200, `{"data":{"issues":{"nodes":[
			{"id":"i2","identifier":"NXT-2","number":2,"title":"Two","priority":0,"createdAt":"2026-10-02T00:00:00Z",
			 "state":{"id":"s1","name":"Ready","type":"unstarted"},"inverseRelations":{"nodes":[]}}],
			"pageInfo":{"hasNextPage":false,"endCursor":"c2"}}}}`
	})
	issues, err := c.IssuesInState(context.Background(), "t1", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[1].Identifier != "NXT-2" {
		t.Fatalf("issues = %+v", issues)
	}
	if b := issues[0].BlockedBy; len(b) != 1 || b[0].Identifier != "NXT-9" || b[0].State.Name != "In Progress" {
		t.Errorf("blockers = %+v", b)
	}
	if issues[0].Number != 1 || issues[0].Priority != 2 {
		t.Errorf("issue = %+v", issues[0])
	}
}

func TestComments(t *testing.T) {
	_, c := newFake(t, func(q string, v map[string]any) (int, string) {
		return 200, `{"data":{"issue":{"comments":{"nodes":[
			{"id":"c2","body":"/restart","createdAt":"2026-10-02T00:00:00Z","user":{"name":"Ann","isMe":false}},
			{"id":"c1","body":"Blocked: …","createdAt":"2026-10-01T00:00:00Z","user":{"name":"GoshaBot","isMe":true}},
			{"id":"c3","body":"deployed","createdAt":"2026-10-03T00:00:00Z","user":null,"botActor":{"name":"GitHub"}}],
			"pageInfo":{"hasNextPage":false}}}}}`
	})
	cs, err := c.Comments(context.Background(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 3 || cs[0].ID != "c1" || cs[1].ID != "c2" {
		t.Fatalf("not sorted oldest first: %+v", cs)
	}
	if cs[0].Human || !cs[1].Human || cs[2].Human {
		t.Errorf("human flags wrong: %+v", cs)
	}
}

func TestWrites(t *testing.T) {
	var seen []string
	_, c := newFake(t, func(q string, v map[string]any) (int, string) {
		switch {
		case strings.Contains(q, "issueUpdate"):
			seen = append(seen, "transition:"+v["state"].(string))
			return 200, `{"data":{"issueUpdate":{"success":true}}}`
		case strings.Contains(q, "commentCreate"):
			seen = append(seen, "comment:"+v["body"].(string))
			return 200, `{"data":{"commentCreate":{"success":true,"comment":{"id":"c9"}}}}`
		case strings.Contains(q, "attachmentCreate"):
			seen = append(seen, "attach:"+v["url"].(string))
			return 200, `{"data":{"attachmentCreate":{"success":false}}}`
		}
		return 400, `{}`
	})
	ctx := context.Background()
	if err := c.Transition(ctx, "i1", "s2"); err != nil {
		t.Fatal(err)
	}
	if id, err := c.CreateComment(ctx, "i1", "hi"); err != nil || id != "c9" {
		t.Fatalf("CreateComment = %q, %v", id, err)
	}
	if err := c.CreateAttachment(ctx, "i1", "https://github.com/o/r/pull/1", "PR #1", ""); err == nil {
		t.Error("expected an unsuccessful attachment to be an error")
	}
	if strings.Join(seen, ",") != "transition:s2,comment:hi,attach:https://github.com/o/r/pull/1" {
		t.Errorf("seen = %v", seen)
	}
}

func TestIssue(t *testing.T) {
	_, c := newFake(t, func(q string, v map[string]any) (int, string) {
		if v["id"] != "NXT-1" {
			return 200, `{"data":{"issue":null}}`
		}
		return 200, `{"data":{"issue":{"id":"i1","identifier":"NXT-1","number":1,"title":"One",
			"state":{"id":"s2","name":"In Progress","type":"started"},"inverseRelations":{"nodes":[]}}}}`
	})
	is, err := c.Issue(context.Background(), "NXT-1")
	if err != nil || is.ID != "i1" || is.State.Name != "In Progress" {
		t.Fatalf("Issue = %+v, %v", is, err)
	}
	if _, err := c.Issue(context.Background(), "NXT-404"); err == nil {
		t.Error("expected an error for a missing issue")
	}
}
