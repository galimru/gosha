// Package config loads and validates the global and per-project config, resolves
// the gosha home directory, and reads secrets with redaction.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Global is the machine-level config, ~/.gosha/config.yaml.
type Global struct {
	Concurrency int         `yaml:"concurrency"`
	Agent       GlobalAgent `yaml:"agent"`
	GitHubApp   GitHubApp   `yaml:"github_app"`
	Telegram    Telegram    `yaml:"telegram"`
	// HeartbeatURL is GET-pinged after every poll, so an outside monitor
	// notices when Gosha stops. Optional.
	HeartbeatURL string `yaml:"heartbeat_url"`
	// RetentionText is how long finished runs' files and idle Blocked
	// workspaces are kept ("30d" by default); Retention is it parsed.
	RetentionText string        `yaml:"retention" default:"30d"`
	Retention     time.Duration `yaml:"-"`
}

// ParseAge parses a duration that may also be in days: "30d", "12h", "90m".
func ParseAge(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

type GlobalAgent struct {
	Sandbox  *bool    `yaml:"sandbox"`
	Writable []string `yaml:"writable"`
}

// SandboxEnabled reports whether agents run sandboxed (the default).
func (a GlobalAgent) SandboxEnabled() bool { return a.Sandbox == nil || *a.Sandbox }

type GitHubApp struct {
	AppID          int64  `yaml:"app_id"`
	PrivateKeyFile string `yaml:"private_key_file" default:"github-app.pem"`
}

type Telegram struct {
	ChatID   int64   `yaml:"chat_id"`
	DigestAt *string `yaml:"digest_at"`
}

// Digest returns the daily digest time ("09:00" by default) or "" if disabled.
func (t Telegram) Digest() string {
	if t.DigestAt == nil {
		return "09:00"
	}
	return *t.DigestAt
}

// LoadGlobal reads and validates the global config.
func LoadGlobal(h Home) (*Global, error) {
	data, err := os.ReadFile(h.Config())
	if err != nil {
		return nil, fmt.Errorf("global config: %w", err)
	}
	return ParseGlobal(h, h.Config(), data)
}

// ParseGlobal decodes and validates global config data.
func ParseGlobal(h Home, file string, data []byte) (*Global, error) {
	g := &Global{}
	if err := decode(file, data, g); err != nil {
		return nil, err
	}
	if g.Concurrency == 0 {
		g.Concurrency = 1
	}
	fe := func(field, msg string) error { return &FieldError{File: file, Field: field, Err: errors.New(msg)} }
	var err error
	if g.Retention, err = ParseAge(g.RetentionText); err != nil || g.Retention == 0 {
		return nil, fe("retention", `must be a duration like "30d" or "12h"`)
	}
	switch {
	case g.Concurrency < 0:
		return nil, fe("concurrency", "must be at least 1")
	case g.GitHubApp.AppID <= 0:
		return nil, fe("github_app.app_id", "required")
	case g.Telegram.ChatID == 0:
		return nil, fe("telegram.chat_id", "required")
	}
	if d := g.Telegram.Digest(); d != "" {
		if _, err := time.Parse("15:04", d); err != nil {
			return nil, fe("telegram.digest_at", `must be "HH:MM" or ""`)
		}
	}
	key := h.Path(g.GitHubApp.PrivateKeyFile)
	f, err := os.Open(key)
	if err != nil {
		return nil, fe("github_app.private_key_file", err.Error())
	}
	defer f.Close()
	if err := CheckPrivate(f); err != nil {
		return nil, fe("github_app.private_key_file", err.Error())
	}
	g.GitHubApp.PrivateKeyFile = key
	return g, nil
}
