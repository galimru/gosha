package pipeline

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/flock"
	"github.com/galimru/gosha/internal/store"
)

// pruneEvery is how often the daemon applies the cleanup rules.
const pruneEvery = time.Hour

// Pruned is one directory the cleanup removed (or, in a dry run, would).
type Pruned struct {
	Project, Ticket string
	Path            string
	Bytes           int64
	Why             string
}

// Prune applies the cleanup rules to a project (or every project, with
// project ""), removing what's been idle longer than olderThan:
//   - the workspace of a Blocked run idle that long (a resume rebuilds it)
//   - a workspace with no open run, left behind by a run that ended
//   - a ticket's runs/ files once it has no open run and its last run
//     finished that long ago
//
// An active run's workspace is never touched, and no run is deleted from
// the store. With dryRun nothing is removed.
func Prune(ctx context.Context, home config.Home, st *store.Store, project string, olderThan time.Duration, dryRun bool) ([]Pruned, error) {
	projects := []string{project}
	if project == "" {
		var err error
		if projects, err = home.Projects(); err != nil {
			return nil, err
		}
	}
	cutoff := time.Now().Add(-olderThan)
	var out []Pruned
	for _, p := range projects {
		got, err := pruneProject(ctx, home, st, p, cutoff, dryRun)
		out = append(out, got...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func pruneProject(ctx context.Context, home config.Home, st *store.Store, project string, cutoff time.Time, dryRun bool) ([]Pruned, error) {
	unlock, err := flock.Lock(home.Project(project).CleanupLock())
	if err != nil {
		return nil, err
	}
	defer unlock()
	var out []Pruned
	remove := func(ticket, path, why string) error {
		size, err := DirSize(path)
		if err != nil {
			return err
		}
		out = append(out, Pruned{Project: project, Ticket: ticket, Path: path, Bytes: size, Why: why})
		if dryRun {
			return nil
		}
		return os.RemoveAll(path)
	}

	tickets, err := subdirs(filepath.Join(home.WorkspacesDir(), project))
	if err != nil {
		return out, err
	}
	for _, t := range tickets {
		ws := home.Workspace(project, t).Dir
		r, err := st.OpenRun(ctx, project, t)
		if err != nil {
			return out, err
		}
		switch {
		case r != nil && Parked(r) && r.UpdatedAt.Before(cutoff):
			err = remove(t, ws, "Blocked since "+r.UpdatedAt.Local().Format(time.DateOnly))
		case r == nil && modifiedBefore(ws, cutoff):
			err = remove(t, ws, "no open run")
		}
		if err != nil {
			return out, err
		}
	}

	tickets, err = subdirs(home.Project(project).Runs())
	if err != nil {
		return out, err
	}
	for _, t := range tickets {
		dir := filepath.Join(home.Project(project).Runs(), t)
		if open, err := st.OpenRun(ctx, project, t); err != nil || open != nil {
			if err != nil {
				return out, err
			}
			continue
		}
		last, err := st.LastRun(ctx, project, t)
		if err != nil {
			return out, err
		}
		switch {
		case last != nil && last.FinishedAt.Before(cutoff):
			err = remove(t, dir, "run "+last.Outcome+" "+last.FinishedAt.Local().Format(time.DateOnly))
		case last == nil && modifiedBefore(dir, cutoff):
			err = remove(t, dir, "no run recorded")
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// PruneLoop applies the cleanup rules to every project every hour, with
// the global retention, until ctx ends.
func (e *Env) PruneLoop(ctx context.Context) {
	for {
		pruned, err := Prune(ctx, e.Home, e.Store, "", e.Global.Retention, false)
		for _, p := range pruned {
			e.Log.Info("pruned", "project", p.Project, "ticket", p.Ticket, "path", p.Path, "bytes", p.Bytes, "why", p.Why)
		}
		if err != nil && ctx.Err() == nil {
			e.Log.Warn("prune", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pruneEvery):
		}
	}
}

// lockWorkspace holds the project's cleanup lock while a pickup writes the
// ticket's workspace and claims its run, so a cleanup never removes a
// workspace a run is about to use.
func (e *Env) lockWorkspace(project string) (unlock func(), err error) {
	return flock.Lock(e.Home.Project(project).CleanupLock())
}

func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func modifiedBefore(path string, cutoff time.Time) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.ModTime().Before(cutoff)
}

// DirSize is the total size of the regular files under dir, 0 if it
// doesn't exist.
func DirSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n, err
}
