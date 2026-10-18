package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gosha.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestMigrateTwice(t *testing.T) {
	s, path := open(t)
	s.Close()
	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s2.Close()
}

func TestRuns(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	if r, err := s.OpenRun(ctx, "p", "NXT-1"); err != nil || r != nil {
		t.Fatalf("no run expected: %v %v", r, err)
	}
	r := &Run{Project: "p", TicketID: "NXT-1", Mode: "fresh", Branch: "gosha/NXT-1-x"}
	if err := s.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.OpenRun(ctx, "p", "NXT-1")
	if err != nil || got == nil || got.ID != r.ID || got.Branch != r.Branch {
		t.Fatalf("OpenRun = %+v, %v", got, err)
	}
	if runs, _ := s.OpenRuns(ctx); len(runs) != 1 {
		t.Errorf("OpenRuns = %d", len(runs))
	}
	if err := s.FinishRun(ctx, r.ID, "delivered"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.OpenRun(ctx, "p", "NXT-1"); got != nil {
		t.Errorf("run still open: %+v", got)
	}
}

func TestCommentCursor(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	if err := s.SetCommentCursor(ctx, "p", "NXT-1", Cursor{CommentID: "c1", At: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCommentCursor(ctx, "p", "NXT-1", Cursor{CommentID: "c2", At: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c, err := s.CommentCursor(ctx, "p", "NXT-1")
	if err != nil || c.CommentID != "c2" || !c.At.Equal(at.Add(time.Hour)) {
		t.Errorf("cursor = %+v, %v", c, err)
	}
}

func TestProjectStateTransitions(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	steps := []struct {
		st      ProjectState
		changed bool
	}{
		{ProjectState{Project: "p", Status: StatusActive}, false}, // first sighting, healthy: nothing to announce
		{ProjectState{Project: "p", Status: StatusPaused, Reason: "bad yaml"}, true},
		{ProjectState{Project: "p", Status: StatusPaused, Reason: "bad yaml"}, false},
		{ProjectState{Project: "p", Status: StatusPaused, Reason: "missing team"}, true},
		{ProjectState{Project: "p", Status: StatusActive, Commit: "abc"}, true},
	}
	for i, step := range steps {
		changed, err := s.SetProjectState(ctx, step.st)
		if err != nil {
			t.Fatal(err)
		}
		if changed != step.changed {
			t.Errorf("step %d: changed = %v, want %v", i, changed, step.changed)
		}
	}
	st, _ := s.ProjectState(ctx, "p")
	if st.Status != StatusActive || st.Commit != "abc" {
		t.Errorf("state = %+v", st)
	}
	s.DeleteProjectState(ctx, "p")
	if st, _ := s.ProjectState(ctx, "p"); st != nil {
		t.Errorf("not deleted: %+v", st)
	}
}

func TestUpdateRunAndBranches(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	r := &Run{Project: "p", TicketID: "NXT-1", Mode: "fresh", Branch: "gosha/NXT-1-x", BaseBranch: "main"}
	if err := s.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Step, r.PRNumber, r.PRURL, r.ImplSession, r.AgentRuns, r.BlockedReason = "review", 5, "https://x/5", "sess", 2, "why"
	if err := s.UpdateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.OpenRun(ctx, "p", "NXT-1")
	if err != nil || got.Step != "review" || got.PRNumber != 5 || got.PRURL != "https://x/5" || got.ImplSession != "sess" ||
		got.AgentRuns != 2 || got.BlockedReason != "why" || got.BaseBranch != "main" {
		t.Fatalf("after update: %+v, %v", got, err)
	}
	s.CreateRun(ctx, &Run{Project: "p", TicketID: "NXT-2", Mode: "fresh", Branch: "gosha/NXT-2-y"})
	s.CreateRun(ctx, &Run{Project: "q", TicketID: "NXT-3", Mode: "fresh", Branch: "other"})
	bs, err := s.Branches(ctx, "p")
	if err != nil || len(bs) != 2 {
		t.Errorf("Branches = %v, %v", bs, err)
	}
}

func TestReviewFields(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	r := &Run{Project: "p", TicketID: "NXT-1", Mode: "fresh"}
	if err := s.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.OpenRun(ctx, "p", "NXT-1"); !got.PRCursor.IsZero() {
		t.Errorf("cursor = %v", got.PRCursor)
	}
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	r.ReviewedSHA, r.ReviewID, r.ReviewResult, r.PRCursor, r.LastReply = "abc", 77, "/r/review.json", at, "fixed it"
	if err := s.UpdateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.OpenRun(ctx, "p", "NXT-1")
	if err != nil || got.ReviewedSHA != "abc" || got.ReviewID != 77 || got.ReviewResult != "/r/review.json" ||
		!got.PRCursor.Equal(at) || got.LastReply != "fixed it" {
		t.Fatalf("after update: %+v, %v", got, err)
	}
}

func TestChecksFields(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	r := &Run{Project: "p", TicketID: "NXT-1", Mode: "fresh"}
	if err := s.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.CheckRound, r.CheckedSHA, r.ChecksResult, r.MergeSHA = 2, "abc", "/r/ci.md", "def"
	if err := s.UpdateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.OpenRun(ctx, "p", "NXT-1")
	if err != nil || got.CheckRound != 2 || got.CheckedSHA != "abc" || got.ChecksResult != "/r/ci.md" || got.MergeSHA != "def" {
		t.Fatalf("after update: %+v, %v", got, err)
	}
}

func TestMigrateFromM2(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gosha.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"migrations/001_init.sql", "migrations/002_m2.sql"} {
		body, _ := migrations.ReadFile(name)
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("PRAGMA user_version = 2")
	db.Exec(`INSERT INTO runs (project, ticket_id, mode, step, created_at, updated_at) VALUES ('p', 'NXT-1', 'fresh', 'review', '', '')`)
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.OpenRun(ctx, "p", "NXT-1")
	if err != nil || got == nil || got.Step != "review" || got.ReviewID != 0 || !got.PRCursor.IsZero() {
		t.Fatalf("migrated run = %+v, %v", got, err)
	}
}
