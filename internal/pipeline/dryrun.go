package pipeline

import (
	"context"
	"fmt"

	"github.com/galimru/gosha/internal/notify"
)

// DryRun does one pickup pass for a project without changing anything in
// Linear or GitHub, and reports what would happen to stdout and Telegram.
// With ticket set, only that Ready ticket is considered.
func (e *Env) DryRun(ctx context.Context, name, ticket string) (string, error) {
	p, err := e.LoadProject(ctx, name)
	if err != nil {
		return "", err
	}
	for _, w := range p.Warnings {
		e.Log.Warn("config warning", "project", name, "warning", w)
	}
	lin, err := e.ConnectLinear(ctx, p)
	if err != nil {
		return "", err
	}
	cands, err := e.candidates(ctx, name, lin, ticket)
	if err != nil {
		return "", err
	}
	plan := Plan(p.Config, lin.States, cands)
	report := Report(p.Commit, plan)
	ev := notify.Event{Project: name, Text: report}
	for _, d := range plan {
		if d.Action == Pickup && d.Issue.URL != "" {
			ev.Links = append(ev.Links, notify.Link{Text: d.Issue.Identifier, URL: d.Issue.URL})
		}
	}
	e.Notify.Send(ev)
	return report, nil
}

// candidates gathers the Ready tickets with what pickup needs to decide on
// them. With ticket set, only that one; it's an error if it isn't Ready.
func (e *Env) candidates(ctx context.Context, name string, lin *Linear, ticket string) ([]Candidate, error) {
	issues, err := lin.Client.IssuesInState(ctx, lin.Team.ID, lin.States.Ready.ID)
	if err != nil {
		return nil, err
	}
	var cands []Candidate
	for _, is := range issues {
		if ticket != "" && is.Identifier != ticket {
			continue
		}
		c := Candidate{Issue: is}
		if c.OpenRun, err = e.Store.OpenRun(ctx, name, is.Identifier); err != nil {
			return nil, err
		}
		cur, err := e.Store.CommentCursor(ctx, name, is.Identifier)
		if err != nil {
			return nil, err
		}
		if c.Comments, err = lin.Client.Comments(ctx, is.ID); err != nil {
			return nil, err
		}
		c.NewComments = NewHumanComments(c.Comments, cur)
		cands = append(cands, c)
	}
	if ticket != "" && len(cands) == 0 {
		return nil, fmt.Errorf("%s is not in %s in team %s", ticket, lin.States.Ready.Name, lin.Team.Key)
	}
	return cands, nil
}
