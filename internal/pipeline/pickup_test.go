package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/linear"
	"github.com/galimru/gosha/internal/store"
)

var (
	states = &linear.States{
		Ready:      linear.State{ID: "s-ready", Name: "Ready", Type: linear.TypeUnstarted},
		InProgress: linear.State{ID: "s-prog", Name: "In Progress", Type: linear.TypeStarted},
		InReview:   linear.State{ID: "s-review", Name: "In Review", Type: linear.TypeStarted},
		Blocked:    linear.State{ID: "s-blocked", Name: "Blocked", Type: linear.TypeStarted},
	}
	done     = linear.State{ID: "s-done", Name: "Done", Type: linear.TypeCompleted}
	canceled = linear.State{ID: "s-canceled", Name: "Canceled", Type: linear.TypeCanceled}
	day0     = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

func project(t *testing.T) *config.Project {
	t.Helper()
	src := "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\n"
	vars := map[string]string{"LINEAR_CLIENT_ID": "a", "LINEAR_CLIENT_SECRET": "b", "NOTION_TOKEN": "c"}
	p, _, err := config.ParseProject([]byte(src), "", config.NewSecrets(nil, nil, vars))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func issue(id string, prio, day int, blockers ...linear.Blocker) linear.Issue {
	return linear.Issue{Identifier: id, Title: "Do " + id, Priority: prio, CreatedAt: day0.AddDate(0, 0, day), BlockedBy: blockers}
}

func TestPlanOrdering(t *testing.T) {
	cands := []Candidate{
		{Issue: issue("NXT-1", 0, 0)}, // no priority: last despite being oldest
		{Issue: issue("NXT-2", 3, 2)},
		{Issue: issue("NXT-3", 1, 5)}, // urgent
		{Issue: issue("NXT-4", 3, 1)}, // same priority as NXT-2, older
		{Issue: issue("NXT-5", 4, 0)},
	}
	var got []string
	for _, d := range Plan(project(t), states, cands) {
		got = append(got, d.Issue.Identifier+":"+string(d.Action))
	}
	want := "NXT-3:pickup NXT-4:queued NXT-2:queued NXT-5:queued NXT-1:queued"
	if strings.Join(got, " ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
}

func TestPlanBlockers(t *testing.T) {
	cands := []Candidate{
		{Issue: issue("NXT-1", 1, 0, linear.Blocker{Identifier: "NXT-9", State: states.InProgress})},
		{Issue: issue("NXT-2", 1, 1, linear.Blocker{Identifier: "NXT-8", State: states.InReview}, linear.Blocker{Identifier: "NXT-7", State: done})},
		{Issue: issue("NXT-3", 1, 2, linear.Blocker{Identifier: "NXT-6", State: canceled}, linear.Blocker{Identifier: "NXT-5", State: states.Ready})},
	}
	ds := Plan(project(t), states, cands)
	if ds[0].Action != Skipped || ds[0].Reason != "blocked by NXT-9 (In Progress)" {
		t.Errorf("NXT-1: %+v", ds[0])
	}
	if ds[1].Action != Pickup {
		t.Errorf("NXT-2 blockers are In Review/Done, should be picked: %+v", ds[1])
	}
	// DESIGN §8.1: only In Review or Done satisfy a blocker, so Canceled doesn't.
	if ds[2].Action != Skipped || ds[2].Reason != "blocked by NXT-6 (Canceled), NXT-5 (Ready)" {
		t.Errorf("NXT-3: %+v", ds[2])
	}
}

func TestPlanModes(t *testing.T) {
	is := issue("NXT-12", 2, 0)
	is.Title = "Add login"
	cases := []struct {
		c            Candidate
		mode, branch string
	}{
		{Candidate{Issue: is}, ModeFresh, "gosha/NXT-12-add-login"},
		{Candidate{Issue: is, NewComments: []linear.Comment{{Body: "please\n /restart \n"}}}, ModeRestart, "gosha/NXT-12-add-login"},
		{Candidate{Issue: is, NewComments: []linear.Comment{{Body: "don't /restart yet"}}}, ModeFresh, "gosha/NXT-12-add-login"},
		{Candidate{Issue: is, OpenRun: &store.Run{Branch: "gosha/NXT-12-add-login-2"}}, ModeResume, "gosha/NXT-12-add-login-2"},
		{Candidate{Issue: is, OpenRun: &store.Run{Branch: "b", PRNumber: 3}}, "resume (review)", "b"},
		{Candidate{Issue: is, OpenRun: &store.Run{Branch: "b", PRNumber: 3}, NewComments: []linear.Comment{{Body: "rename it"}}}, "resume (address)", "b"},
		{Candidate{Issue: is, Delivered: &store.Run{PRNumber: 1}, NewComments: []linear.Comment{{Body: "also log out"}}}, ModeFollowUp, "gosha/NXT-12-add-login"},
		{Candidate{Issue: is, Delivered: &store.Run{PRNumber: 1}}, ModeFollowUp + ", blocked: no comment says what to change", "gosha/NXT-12-add-login"},
		{Candidate{Issue: is, Delivered: &store.Run{PRNumber: 1}, NewComments: []linear.Comment{{Body: "/restart"}}}, ModeRestart, "gosha/NXT-12-add-login"},
	}
	for i, c := range cases {
		d := Plan(project(t), states, []Candidate{c.c})[0]
		if d.Mode != c.mode || d.Branch != c.branch {
			t.Errorf("case %d: mode %q branch %q, want %q %q", i, d.Mode, d.Branch, c.mode, c.branch)
		}
	}
}

func TestNewHumanComments(t *testing.T) {
	cs := []linear.Comment{
		{ID: "1", Human: true, CreatedAt: day0},
		{ID: "2", Human: false, CreatedAt: day0.Add(2 * time.Hour)},
		{ID: "3", Human: true, CreatedAt: day0.Add(3 * time.Hour)},
	}
	if got := NewHumanComments(cs, nil); len(got) != 2 {
		t.Errorf("no cursor: %v", got)
	}
	got := NewHumanComments(cs, &store.Cursor{CommentID: "2", At: day0.Add(2 * time.Hour)})
	if len(got) != 1 || got[0].ID != "3" {
		t.Errorf("after cursor: %v", got)
	}
}

func TestReport(t *testing.T) {
	cands := []Candidate{
		{Issue: issue("NXT-12", 1, 0)},
		{Issue: issue("NXT-15", 2, 0)},
		{Issue: issue("NXT-11", 0, 0)},
		{Issue: issue("NXT-14", 2, 0, linear.Blocker{Identifier: "NXT-9", State: states.InProgress})},
	}
	got := Report("a1b2c3d4e5f6", Plan(project(t), states, cands))
	want := `dry-run @ a1b2c3d · 4 Ready
▶ would pick up NXT-12 Do NXT-12 (Urgent) · fresh · gosha/NXT-12-do-nxt-12
queued: NXT-15 (High), NXT-11 (None)
skipped: NXT-14 · blocked by NXT-9 (In Progress)`
	if got != want {
		t.Errorf("Report =\n%s\nwant\n%s", got, want)
	}
	if got := Report("a1b2c3d", nil); got != "dry-run @ a1b2c3d · nothing Ready" {
		t.Errorf("empty report = %q", got)
	}
}
