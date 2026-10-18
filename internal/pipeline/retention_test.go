package pipeline

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/store"
)

func TestPrune(t *testing.T) {
	ctx := context.Background()
	home := config.Home(t.TempDir())
	os.MkdirAll(home.Project("demo").Repo(), 0o700)
	dbPath := filepath.Join(t.TempDir(), "gosha.db")
	st, err := store.Open(ctx, dbPath, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := time.Now().Add(-40 * 24 * time.Hour)
	mkdir := func(path string, mod time.Time) {
		os.MkdirAll(path, 0o700)
		os.WriteFile(filepath.Join(path, "f"), []byte("12345"), 0o600)
		os.Chtimes(path, mod, mod)
	}
	run := func(ticket, step string) *store.Run {
		r := &store.Run{Project: "demo", TicketID: ticket, Mode: ModeFresh, Step: step}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	backdate := func(column, ticket string) {
		// The store stamps runs with the current time; age them directly.
		if _, err := db.ExecContext(ctx, "UPDATE runs SET "+column+" = ? WHERE ticket_id = ?", old.UTC().Format(time.RFC3339Nano), ticket); err != nil {
			t.Fatal(err)
		}
	}
	ws := func(ticket string) string { return home.Workspace("demo", ticket).Dir }
	runs := func(ticket string) string { return filepath.Join(home.Project("demo").Runs(), ticket) }

	// NXT-1: Blocked for 40 days: workspace goes, runs/ stays (run open).
	run("NXT-1", StepBlocked)
	backdate("updated_at", "NXT-1")
	mkdir(ws("NXT-1"), old)
	mkdir(runs("NXT-1"), old)
	// NXT-2: Blocked yesterday: kept.
	run("NXT-2", StepBlocked)
	mkdir(ws("NXT-2"), old)
	// NXT-3: active for 40 days (waiting for CI): never touched.
	run("NXT-3", StepChecks)
	backdate("updated_at", "NXT-3")
	mkdir(ws("NXT-3"), old)
	// NXT-4: delivered 40 days ago: runs/ goes; NXT-5 delivered today: kept.
	r := run("NXT-4", StepDeploy)
	st.FinishRun(ctx, r.ID, OutcomeDelivered)
	backdate("finished_at", "NXT-4")
	mkdir(runs("NXT-4"), old)
	r = run("NXT-5", StepDeploy)
	st.FinishRun(ctx, r.ID, OutcomeDelivered)
	mkdir(runs("NXT-5"), old)
	// NXT-6: a leftover workspace with no run, old; NXT-7 new (a pickup
	// about to claim it).
	mkdir(ws("NXT-6"), old)
	mkdir(ws("NXT-7"), time.Now())

	dry, err := Prune(ctx, home, st, "", 30*24*time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range dry {
		rel, _ := filepath.Rel(home.Dir(), p.Path)
		got = append(got, rel)
		if p.Bytes != 5 {
			t.Errorf("%s: %d bytes", rel, p.Bytes)
		}
	}
	slices.Sort(got)
	want := []string{"projects/demo/runs/NXT-4", "workspaces/demo/NXT-1", "workspaces/demo/NXT-6"}
	if !slices.Equal(got, want) {
		t.Fatalf("pruned = %q, want %q", got, want)
	}
	if _, err := os.Stat(ws("NXT-1")); err != nil {
		t.Fatal("dry run removed something")
	}
	if _, err := Prune(ctx, home, st, "demo", 30*24*time.Hour, false); err != nil {
		t.Fatal(err)
	}
	for _, p := range want {
		if _, err := os.Stat(filepath.Join(home.Dir(), p)); !os.IsNotExist(err) {
			t.Errorf("%s not removed", p)
		}
	}
	for _, p := range []string{ws("NXT-2"), ws("NXT-3"), ws("NXT-7"), runs("NXT-1"), runs("NXT-5")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed", p)
		}
	}
	if r, _ := st.LastRun(ctx, "demo", "NXT-4"); r == nil {
		t.Error("run record deleted")
	}
}
