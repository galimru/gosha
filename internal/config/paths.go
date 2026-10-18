package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Home is Gosha's single home directory (DESIGN §11.3).
type Home string

// ResolveHome picks the home directory: the --home flag, then GOSHA_HOME,
// then ~/.gosha of the running user.
func ResolveHome(flag string) (Home, error) {
	dir := flag
	if dir == "" {
		dir = os.Getenv("GOSHA_HOME")
	}
	if dir == "" {
		uh, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		dir = filepath.Join(uh, ".gosha")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return Home(abs), nil
}

func (h Home) Dir() string         { return string(h) }
func (h Home) Config() string      { return filepath.Join(string(h), "config.yaml") }
func (h Home) Secrets() string     { return filepath.Join(string(h), "secrets.env") }
func (h Home) DB() string          { return filepath.Join(string(h), "gosha.db") }
func (h Home) LogFile() string     { return filepath.Join(string(h), "logs", "gosha.log") }
func (h Home) ProjectsDir() string { return filepath.Join(string(h), "projects") }

// WorkspacesDir holds every ticket's agent workspace: the only part of
// the home agents can reach (DESIGN §7.1).
func (h Home) WorkspacesDir() string { return filepath.Join(string(h), "workspaces") }

// Path resolves p relative to the home directory unless it is absolute.
func (h Home) Path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(string(h), p)
}

// Project returns the paths of one project's directory.
func (h Home) Project(name string) ProjectPaths {
	return ProjectPaths{Dir: filepath.Join(h.ProjectsDir(), name)}
}

// Workspace returns the paths of a ticket's agent workspace.
func (h Home) Workspace(project, ticket string) Workspace {
	return Workspace{Dir: filepath.Join(h.WorkspacesDir(), project, ticket)}
}

// EnsureDir creates the home directory with mode 700 if it doesn't exist.
func (h Home) EnsureDir() error {
	if err := os.MkdirAll(string(h), 0o700); err != nil {
		return fmt.Errorf("create home: %w", err)
	}
	return nil
}

// Projects lists registered projects: directories under projects/ that
// contain a repo/ clone (DESIGN §5.1). Names starting with '.' are
// in-progress adds and are skipped.
func (h Home) Projects() ([]string, error) {
	entries, err := os.ReadDir(h.ProjectsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if fi, err := os.Stat(h.Project(e.Name()).Repo()); err == nil && fi.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// ProjectPaths are the paths under projects/<name>/.
type ProjectPaths struct{ Dir string }

func (p ProjectPaths) Secrets() string { return filepath.Join(p.Dir, "secrets.env") }
func (p ProjectPaths) Repo() string    { return filepath.Join(p.Dir, "repo") }
func (p ProjectPaths) Context() string { return filepath.Join(p.Dir, "context") }
func (p ProjectPaths) Runs() string    { return filepath.Join(p.Dir, "runs") }

// RunLock is held by the one process running the project (the daemon or
// gosha run).
func (p ProjectPaths) RunLock() string { return filepath.Join(p.Dir, "run.lock") }

// CleanupLock is held while workspaces are pruned, and by a pickup from
// before it writes the ticket's workspace until its run is claimed.
func (p ProjectPaths) CleanupLock() string { return filepath.Join(p.Dir, "cleanup.lock") }

// Claude is the project's own CLAUDE_CONFIG_DIR: agent sessions persist
// there for --resume, and the user's ~/.claude never applies (DESIGN §11.2).
func (p ProjectPaths) Claude() string { return filepath.Join(p.Dir, "claude") }

// Workspace is what agents work in for one ticket (DESIGN §11.3).
// Everything in it can be rebuilt.
type Workspace struct{ Dir string }

// Checkout is the implementer's clone.
func (w Workspace) Checkout() string { return filepath.Join(w.Dir, "checkout") }

// Review is the reviewer's clone, only during a review.
func (w Workspace) Review() string { return filepath.Join(w.Dir, "review") }

// Context is the pickup's frozen copy of the project context.
func (w Workspace) Context() string { return filepath.Join(w.Dir, "context") }

// Tmp is agent pass n's TMPDIR, which also holds its result file.
func (w Workspace) Tmp(n int) string { return filepath.Join(w.Dir, "tmp", strconv.Itoa(n)) }
