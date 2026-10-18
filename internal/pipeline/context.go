package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/galimru/gosha/internal/notion"
	"github.com/galimru/gosha/internal/tmpl"
)

// Mirror is the project's Notion mirror.
func (e *Env) Mirror(p *Project) (*notion.Mirror, error) {
	root, err := notion.ParsePageID(p.Config.Notion.RootPage)
	if err != nil {
		return nil, err
	}
	var api notion.API
	if e.Notion != nil {
		api = e.Notion(p.Creds.NotionToken)
	} else {
		api = notion.New(p.Creds.NotionToken)
	}
	return &notion.Mirror{Dir: e.Home.Project(p.Name).Context(), Root: root, API: api}, nil
}

// refreshContext brings the project's mirror up to date before a pickup.
// Without access to the root page, the project pauses (fail closed, like an
// invalid config) until a later pickup's refresh succeeds.
// Any other failure keeps the last mirror, and is an error only when there
// is none yet.
func (e *Env) refreshContext(ctx context.Context, p *Project) error {
	m, err := e.Mirror(p)
	if err != nil {
		return e.pauseNotion(ctx, p, fmt.Errorf("notion.root_page: %w", err))
	}
	start := time.Now()
	st, err := m.Refresh(ctx)
	switch {
	case errors.Is(err, notion.ErrNoAccess):
		return e.pauseNotion(ctx, p, fmt.Errorf("can't read the Notion root page (is it shared with the integration?): %w", err))
	case err != nil && ctx.Err() != nil:
		return err
	case err != nil:
		if _, ierr := m.Info(); ierr != nil {
			return fmt.Errorf("%s: context refresh: %w", p.Name, err)
		}
		e.Log.Warn("context refresh failed, using the last mirror", "project", p.Name, "err", e.Redactor.Redact(err.Error()))
		return nil
	}
	if st.Changed() {
		e.Log.Info("context refreshed", "project", p.Name, "changes", st.String(), "took", time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// freezeContext copies the mirror for a ticket's pickup to its
// workspace, so the agents in it see one consistent version.
func (e *Env) freezeContext(p *Project, ticketID string) (tmpl.Context, error) {
	m, err := e.Mirror(p)
	if err != nil {
		return tmpl.Context{}, err
	}
	home, err := e.realHome()
	if err != nil {
		return tmpl.Context{}, err
	}
	dir := home.Workspace(p.Name, ticketID).Context()
	idx, err := m.Freeze(dir)
	if err != nil {
		return tmpl.Context{}, fmt.Errorf("freeze context: %w", err)
	}
	return tmpl.Context{Dir: dir, Index: idx}, nil
}
