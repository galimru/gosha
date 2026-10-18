// Package agent runs the claude CLI headless for Gosha's agents:
// generated settings, a scrubbed environment, the result contract and
// usage-limit detection.
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Paths are the directories one agent run touches. Every path is absolute.
type Paths struct {
	// Home is Gosha's home (~/.gosha): denied, except for its workspaces.
	Home string
	// Workspace is the ticket's workspace (workspaces/<project>/<ticket>),
	// which holds every path below except the caches.
	Workspace string
	// Checkout is the agent's clone: the working dir, read/write for the
	// implementer and read-only for the reviewer.
	Checkout string
	// RunDir is the run's temp dir and holds the result file: read/write.
	RunDir string
	// Context is the pickup's frozen copy of the project context:
	// read-only. Empty when there is none.
	Context string
	// UserHome is the service user's home, for toolchain caches.
	UserHome string
	// Writable are extra writable paths (global agent.writable).
	Writable []string
}

// Caches are the toolchain caches agents may write by default. Go's build
// cache lives under ~/Library/Caches on macOS and ~/.cache on Linux.
func (p Paths) Caches() []string {
	rel := []string{".cache", "go", ".npm"}
	if runtime.GOOS == "darwin" {
		rel = append(rel, "Library/Caches")
	}
	var out []string
	for _, r := range rel {
		out = append(out, filepath.Join(p.UserHome, r))
	}
	for _, w := range p.Writable {
		if rest, ok := strings.CutPrefix(w, "~/"); ok {
			w = filepath.Join(p.UserHome, rest)
		}
		out = append(out, w)
	}
	return out
}

// writable is every path an agent in role may write. The reviewer only
// reads: it may write its run dir (TMPDIR and the result file) and
// nothing else.
func (p Paths) writable(role string) []string {
	if role == RoleReviewer {
		return []string{p.RunDir}
	}
	return append([]string{p.Checkout, p.RunDir}, p.Caches()...)
}

// EnsureDirs creates every writable directory: the sandbox can't create
// the missing parent of an allowed directory (SPIKES §1c).
func (p Paths) EnsureDirs() error {
	for _, d := range p.writable(RoleImplementer) {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Agent roles.
const (
	RoleImplementer = "implement"
	RoleReviewer    = "review"
)

// ReviewerDisallowed are the tools removed from the reviewer.
var ReviewerDisallowed = []string{"Edit", "Write", "NotebookEdit"}

// Settings builds the settings file for an agent run. The
// reviewer gets no Edit allows and, in the sandbox, no writable checkout;
// its edit tools are also removed outright (ReviewerDisallowed), which
// holds in every permission mode.
//
// The two layers need different shapes (SPIKES §1i): the OS sandbox denies
// all of Home and re-allows the ticket's workspace, but for permission
// rules a deny always beats an allow, so file tools get denies for every
// top-level entry of Home except workspaces/ instead.
func Settings(p Paths, role string, sandbox bool) ([]byte, error) {
	allow := []string{"Read", "Bash", "WebFetch", "WebSearch", "Task", "TodoWrite", "ToolSearch"}
	if role != RoleReviewer {
		allow = append(allow, "Edit("+rule(p.Checkout)+"/**)", "Edit("+rule(p.RunDir)+"/**)")
	}
	var deny []string
	denied, err := outsideWorkspaces(p.Home)
	if err != nil {
		return nil, err
	}
	for _, s := range denied {
		deny = append(deny, "Read("+rule(s)+")", "Read("+rule(s)+"/**)", "Edit("+rule(s)+")", "Edit("+rule(s)+"/**)")
	}
	if p.Context != "" {
		deny = append(deny, "Edit("+rule(p.Context)+")", "Edit("+rule(p.Context)+"/**)")
	}
	s := map[string]any{
		"permissions": map[string]any{"allow": allow, "deny": deny},
	}
	if sandbox {
		fs := map[string]any{
			"denyRead":   []string{p.Home},
			"allowRead":  []string{p.Workspace},
			"allowWrite": p.writable(role),
		}
		if p.Context != "" {
			fs["denyWrite"] = []string{p.Context}
		}
		s["sandbox"] = map[string]any{
			"enabled":                  true,
			"failIfUnavailable":        true,
			"autoAllowBashIfSandboxed": true,
			"allowUnsandboxedCommands": false,
			"filesystem":               fs,
			"network":                  map[string]any{"allowedDomains": []string{"*"}},
		}
	}
	return json.MarshalIndent(s, "", "  ")
}

// rule is an absolute path in permission-rule syntax ("//abs/path").
func rule(path string) string { return "/" + filepath.Clean(path) }

func nonEmpty(paths ...string) []string {
	return slices.DeleteFunc(paths, func(s string) bool { return s == "" })
}

// outsideWorkspaces lists every top-level entry of home except
// workspaces/: secrets, the database, projects, logs and anything added
// later. Denying them leaves agents only the workspaces.
func outsideWorkspaces(home string) ([]string, error) {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Name() != "workspaces" {
			out = append(out, filepath.Join(home, e.Name()))
		}
	}
	return out, nil
}
