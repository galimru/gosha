// Package pipeline is the per-ticket pipeline (DESIGN §8). M1 has pickup
// selection and the dry-run report only.
package pipeline

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/linear"
	"github.com/galimru/gosha/internal/store"
	"github.com/galimru/gosha/internal/tmpl"
)

// Action is what pickup would do with a Ready ticket.
type Action string

const (
	Pickup  Action = "pickup"
	Queued  Action = "queued"
	Skipped Action = "skipped"
)

// Modes (DESIGN §8.1).
const (
	ModeFresh   = "fresh"
	ModeRestart = "fresh (restart)"
	ModeResume  = "resume"
)

// Candidate is a Ready ticket with what pickup needs to decide on it.
type Candidate struct {
	Issue linear.Issue
	// NewComments are human comments since Gosha's last run on the ticket.
	NewComments []linear.Comment
	OpenRun     *store.Run
}

// Decision is pickup's verdict on one ticket.
type Decision struct {
	Issue  linear.Issue
	Action Action
	Reason string // why it's skipped
	Mode   string
	Branch string
}

// Plan orders Ready tickets and decides which one an idle project picks up
// (DESIGN §8.1): tickets with unfinished blockers are skipped, the rest are
// ordered by priority (Urgent first, None last) and then by age.
func Plan(p *config.Project, st *linear.States, cands []Candidate) []Decision {
	sorted := slices.Clone(cands)
	slices.SortStableFunc(sorted, func(a, b Candidate) int {
		if c := cmp.Compare(priorityRank(a.Issue.Priority), priorityRank(b.Issue.Priority)); c != 0 {
			return c
		}
		return a.Issue.CreatedAt.Compare(b.Issue.CreatedAt)
	})
	var out []Decision
	picked := false
	for _, c := range sorted {
		d := Decision{Issue: c.Issue}
		if open := openBlockers(c.Issue, st); len(open) > 0 {
			d.Action, d.Reason = Skipped, "blocked by "+strings.Join(open, ", ")
			out = append(out, d)
			continue
		}
		d.Mode, d.Branch = mode(p, c)
		if picked {
			d.Action = Queued
		} else {
			d.Action, picked = Pickup, true
		}
		out = append(out, d)
	}
	return out
}

// priorityRank puts Linear's "No priority" (0) after Low (4).
func priorityRank(p int) int {
	if p <= 0 {
		return 5
	}
	return p
}

// openBlockers lists blockers that are neither In Review (merged) nor Done.
func openBlockers(is linear.Issue, st *linear.States) []string {
	var open []string
	for _, b := range is.BlockedBy {
		if b.State.IsDone() || b.State.ID == st.InReview.ID {
			continue
		}
		open = append(open, fmt.Sprintf("%s (%s)", b.Identifier, b.State.Name))
	}
	return open
}

func mode(p *config.Project, c Candidate) (mode, branch string) {
	if c.OpenRun != nil {
		return ModeResume, c.OpenRun.Branch
	}
	m := ModeFresh
	if HasRestart(c.NewComments) {
		m = ModeRestart
	}
	branch, err := p.BranchName(Ticket(c.Issue, nil))
	if err != nil {
		branch = "(invalid branch name: " + err.Error() + ")"
	}
	return m, branch
}

// HasRestart reports a /restart command on a line of its own.
func HasRestart(cs []linear.Comment) bool {
	for _, c := range cs {
		for _, line := range strings.Split(c.Body, "\n") {
			if strings.TrimSpace(line) == "/restart" {
				return true
			}
		}
	}
	return false
}

// NewHumanComments keeps human comments posted after the cursor.
func NewHumanComments(cs []linear.Comment, cur *store.Cursor) []linear.Comment {
	var out []linear.Comment
	for _, c := range cs {
		if !c.Human || (cur != nil && !c.CreatedAt.After(cur.At)) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Ticket converts an issue to the template data model.
func Ticket(is linear.Issue, comments []linear.Comment) tmpl.Ticket {
	team, _, _ := strings.Cut(is.Identifier, "-")
	t := tmpl.Ticket{
		ID: is.Identifier, Team: team, Number: is.Number, Title: is.Title,
		Description: is.Description, URL: is.URL,
	}
	for _, c := range comments {
		t.Comments = append(t.Comments, tmpl.Comment{Author: c.Author, Body: c.Body})
	}
	return t
}
