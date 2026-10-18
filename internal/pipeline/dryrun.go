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
	issues, err := lin.Client.IssuesInState(ctx, lin.Team.ID, lin.States.Ready.ID)
	if err != nil {
		return "", err
	}

	var cands []Candidate
	for _, is := range issues {
		if ticket != "" && is.Identifier != ticket {
			continue
		}
		c := Candidate{Issue: is}
		if c.OpenRun, err = e.Store.OpenRun(ctx, name, is.Identifier); err != nil {
			return "", err
		}
		cur, err := e.Store.CommentCursor(ctx, name, is.Identifier)
		if err != nil {
			return "", err
		}
		comments, err := lin.Client.Comments(ctx, is.ID)
		if err != nil {
			return "", err
		}
		c.NewComments = NewHumanComments(comments, cur)
		cands = append(cands, c)
	}
	if ticket != "" && len(cands) == 0 {
		return "", fmt.Errorf("%s is not in %s in team %s", ticket, lin.States.Ready.Name, lin.Team.Key)
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
