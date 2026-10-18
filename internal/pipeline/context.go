package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/notion"
	"github.com/galimru/gosha/internal/store"
	"github.com/galimru/gosha/internal/tmpl"
)

// refreshEvery is how often an idle project's context is refreshed. A
// pickup always refreshes first (DESIGN §6.2).
const refreshEvery = 15 * time.Minute

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

// contextDue reports whether the idle refresh is due: the mirror is
// missing or old, or the project is paused (possibly over Notion access,
// which only a refresh can clear).
func (e *Env) contextDue(ctx context.Context, p *Project) bool {
	if ps, _ := e.Store.ProjectState(ctx, p.Name); ps != nil && ps.Status == store.StatusPaused {
		return true
	}
	m, err := e.Mirror(p)
	if err != nil {
		return true
	}
	info, err := m.Info()
	return err != nil || time.Since(info.Refreshed) >= refreshEvery
}

// refreshContext brings the project's mirror up to date. Without access to
// the root page, the project pauses (fail closed, like an invalid config).
// Any other failure keeps the last mirror, and is an error only when there
// is none yet.
func (e *Env) refreshContext(ctx context.Context, p *Project) error {
	m, err := e.Mirror(p)
	if err != nil {
		return e.pause(ctx, p, fmt.Errorf("notion.root_page: %w", err))
	}
	start := time.Now()
	st, err := m.Refresh(ctx)
	switch {
	case errors.Is(err, notion.ErrNoAccess):
		return e.pause(ctx, p, fmt.Errorf("can't read the Notion root page (is it shared with the integration?): %w", err))
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
		e.Notify.Send(notify.Event{Project: p.Name, Text: "context · " + st.String()})
	}
	return nil
}

// freezeContext copies the mirror for a ticket's pickup to
// runs/<ticket>/context, so the agents in it see one consistent version.
func (e *Env) freezeContext(p *Project, ticketID string) (tmpl.Context, error) {
	m, err := e.Mirror(p)
	if err != nil {
		return tmpl.Context{}, err
	}
	// The sandbox matches real paths, so resolve symlinks in the home.
	home, err := filepath.EvalSymlinks(e.Home.Dir())
	if err != nil {
		return tmpl.Context{}, err
	}
	dir := filepath.Join(home, "projects", p.Name, "runs", ticketID, "context")
	idx, err := m.Freeze(dir)
	if err != nil {
		return tmpl.Context{}, fmt.Errorf("freeze context: %w", err)
	}
	return tmpl.Context{Dir: dir, Index: idx}, nil
}
