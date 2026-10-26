package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// SecretsTemplate is a new shared secrets.env: every key Gosha reads.
const SecretsTemplate = `# Gosha secrets: KEY=value, one per line. Keep this file chmod 600.
# Gosha reads these keys by name; a project's own secrets.env overrides any of them.
LINEAR_CLIENT_ID=
LINEAR_CLIENT_SECRET=
NOTION_TOKEN=
TELEGRAM_BOT_TOKEN=
CLAUDE_CODE_OAUTH_TOKEN=
`

// StarterGlobal is a new config.yaml: the two required fields, and every
// optional one commented out with its default.
const StarterGlobal = `# Gosha's host config. The options are described in DESIGN §5.1.

# concurrency: 1               # max simultaneous agent runs across all projects

# agent:
#   sandbox: true              # false = no sandbox and auto mode
#   writable: []               # extra paths agents may write

github_app:
  app_id: 0
  # private_key_file: github-app.pem   # relative to this directory; chmod 600

telegram:
  chat_id: 0
  # digest_at: "09:00"         # daily digest, local time; empty disables it

# heartbeat_url: ""            # GET every 30s, for an outside monitor
# retention: 30d               # how long finished runs' files and idle Blocked workspaces are kept
`

// SetSecret sets key in an env file, keeping every other line. A missing
// file is created from SecretsTemplate. The file is written with mode 600.
func SetSecret(path, key, value string) error {
	if !envKey.MatchString(key) || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("invalid secret %s", key)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = []byte(SecretsTemplate), nil
	}
	if err != nil {
		return err
	}
	lines := strings.SplitAfter(string(data), "\n")
	found := false
	for i, l := range lines {
		k, _, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && strings.TrimSpace(strings.TrimPrefix(k, "export ")) == key {
			lines[i] = key + "=" + value + "\n"
			found = true
		}
	}
	out := strings.Join(lines, "")
	if !found {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += key + "=" + value + "\n"
	}
	return WriteFile(path, []byte(out), 0o600)
}

// GlobalValue reads one dotted field of config.yaml ("telegram.chat_id")
// without validating the rest, so a half-written file can still be read.
// It is "" when the file or the field is missing.
func GlobalValue(path, field string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	_, v := lookup(&root, strings.Split(field, "."))
	if v == nil || v.Kind != yaml.ScalarNode || v.Tag == "!!null" {
		return "", nil
	}
	return v.Value, nil
}

// SetGlobal sets one field of config.yaml, under a top-level mapping
// ("github_app.app_id"), to a plain scalar. Only that value's text
// changes: comments and layout are kept. A missing file is created from
// StarterGlobal.
func SetGlobal(path, field, value string) error {
	parent, child, ok := strings.Cut(field, ".")
	if !ok || strings.Contains(child, ".") {
		return fmt.Errorf("SetGlobal: %q is not section.key", field)
	}
	data, err := os.ReadFile(path)
	perm := os.FileMode(0o644)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = []byte(StarterGlobal), nil
	} else if fi, serr := os.Stat(path); serr == nil {
		perm = fi.Mode().Perm()
	}
	if err != nil {
		return err
	}
	out, err := setYAML(string(data), parent, child, value)
	if err != nil {
		return fmt.Errorf("%s: %w; set %s: %s by hand", path, err, field, value)
	}
	if got, err := valueIn(out, field); err != nil || got != value {
		return fmt.Errorf("%s: couldn't set %s; set it by hand to %s", path, field, value)
	}
	return WriteFile(path, []byte(out), perm)
}

func setYAML(data, parent, child, value string) (string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(data), &root); err != nil {
		return "", err
	}
	lines := strings.SplitAfter(data, "\n")
	pk, pv := lookup(&root, []string{parent})
	switch {
	case pk == nil:
		// No section yet: append it.
		if data != "" && !strings.HasSuffix(data, "\n") {
			data += "\n"
		}
		return data + "\n" + parent + ":\n  " + child + ": " + value + "\n", nil
	case pv.Kind == yaml.ScalarNode && pv.Tag == "!!null" && pv.Value == "":
		// "parent:" with nothing under it.
		return insertLine(lines, pk.Line, "  "+child+": "+value+"\n"), nil
	case pv.Kind != yaml.MappingNode || pv.Style&yaml.FlowStyle != 0:
		return "", fmt.Errorf("%s isn't a block mapping", parent)
	}
	ck, cv := lookup(pv, []string{child})
	if ck == nil {
		indent := strings.Repeat(" ", pv.Content[0].Column-1)
		return insertLine(lines, pk.Line, indent+child+": "+value+"\n"), nil
	}
	if cv.Kind != yaml.ScalarNode || cv.Line != ck.Line {
		return "", fmt.Errorf("%s.%s isn't a plain value", parent, child)
	}
	i := cv.Line - 1
	line := strings.TrimRight(lines[i], "\n")
	start := cv.Column - 1
	rest := line[start:]
	tail := ""
	if c := strings.Index(rest, " #"); c >= 0 {
		// Keep the comment's column when the new value fits.
		tail = strings.Repeat(" ", max(c+1-len(value), 1)) + rest[c+1:]
	}
	lines[i] = line[:start] + value + tail + "\n"
	return strings.Join(lines, ""), nil
}

// insertLine inserts s after line n (1-based).
func insertLine(lines []string, n int, s string) string {
	if n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
		lines[n-1] += "\n"
	}
	out := append([]string{}, lines[:n]...)
	out = append(out, s)
	return strings.Join(append(out, lines[n:]...), "")
}

// lookup follows keys through nested mappings, returning the last key's
// key and value nodes, or nils.
func lookup(n *yaml.Node, keys []string) (*yaml.Node, *yaml.Node) {
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil, nil
		}
		n = n.Content[0]
	}
	var k *yaml.Node
	for _, key := range keys {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil, nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				k, next = n.Content[i], n.Content[i+1]
			}
		}
		if next == nil {
			return nil, nil
		}
		n = next
	}
	return k, n
}

func valueIn(data, field string) (string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(data), &root); err != nil {
		return "", err
	}
	_, v := lookup(&root, strings.Split(field, "."))
	if v == nil {
		return "", nil
	}
	return v.Value, nil
}

// WriteFile replaces path atomically with data, with mode perm.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
