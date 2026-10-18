// Package agent runs the claude CLI headless for Gosha's agents (DESIGN §7):
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
	// Home is Gosha's home (~/.gosha): denied, except for the paths below.
	Home string
	// Checkout is the ticket checkout: the working dir, read/write.
	Checkout string
	// RunDir is the run's temp dir and holds the result file: read/write.
	RunDir string
	// Context is the run's frozen copy of the project context: read-only.
	// Empty until the Notion mirror exists (M5).
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

// writable is every path the agent may write.
func (p Paths) writable() []string {
	return append([]string{p.Checkout, p.RunDir}, p.Caches()...)
}

// EnsureDirs creates every writable directory: the sandbox can't create
// the missing parent of an allowed directory (SPIKES §1c).
func (p Paths) EnsureDirs() error {
	for _, d := range p.writable() {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Agent roles (DESIGN §7).
const (
	RoleImplementer = "implement"
	RoleReviewer    = "review"
)

// ReviewerDisallowed are the tools removed from the reviewer.
var ReviewerDisallowed = []string{"Edit", "Write", "NotebookEdit"}

// Settings builds the settings file for an agent run (DESIGN §7.1). The
// reviewer gets no Edit allows; its edit tools are also removed outright
// (ReviewerDisallowed), which holds in every permission mode.
//
// The two layers need different shapes (SPIKES §1i): the OS sandbox denies
// all of Home and re-allows the run's paths with allowRead/allowWrite, but
// for permission rules a deny always beats an allow, so file tools get
// denies for everything in Home *around* the run's paths instead.
func Settings(p Paths, role string, sandbox bool) ([]byte, error) {
	allow := []string{"Read", "Bash", "WebFetch", "WebSearch", "Task", "TodoWrite", "ToolSearch"}
	if role != RoleReviewer {
		allow = append(allow, "Edit("+rule(p.Checkout)+"/**)", "Edit("+rule(p.RunDir)+"/**)")
	}
	var deny []string
	siblings, err := around(p.Home, []string{p.Checkout, p.RunDir, p.Context})
	if err != nil {
		return nil, err
	}
	for _, s := range siblings {
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
			"allowRead":  nonEmpty(p.Checkout, p.RunDir, p.Context),
			"allowWrite": p.writable(),
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

// around lists the entries under root that are neither one of keep nor an
// ancestor of one: denying exactly these hides everything in root except
// keep. Entries are listed now, so keep paths must already exist.
func around(root string, keep []string) ([]string, error) {
	root = filepath.Clean(root)
	var kept []string
	for _, k := range nonEmpty(keep...) {
		k = filepath.Clean(k)
		if strings.HasPrefix(k, root+string(filepath.Separator)) {
			kept = append(kept, k)
		}
	}
	var out []string
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			switch {
			case slices.Contains(kept, path):
			case slices.ContainsFunc(kept, func(k string) bool { return strings.HasPrefix(k, path+string(filepath.Separator)) }):
				if err := walk(path); err != nil {
					return err
				}
			default:
				out = append(out, path)
			}
		}
		return nil
	}
	if len(kept) == 0 {
		return []string{root}, nil
	}
	return out, walk(root)
}
