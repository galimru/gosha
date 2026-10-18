package config

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Global is the machine-level config, ~/.gosha/config.yaml (DESIGN §5.1).
type Global struct {
	Concurrency int         `yaml:"concurrency"`
	Agent       GlobalAgent `yaml:"agent"`
	GitHubApp   GitHubApp   `yaml:"github_app"`
	Claude      Claude      `yaml:"claude"`
	Telegram    Telegram    `yaml:"telegram"`
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

type Claude struct {
	OAuthToken string `yaml:"oauth_token" secret:"true" default:"${CLAUDE_CODE_OAUTH_TOKEN}"`
}

type Telegram struct {
	BotToken string  `yaml:"bot_token" secret:"true" default:"${TELEGRAM_BOT_TOKEN}"`
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
func LoadGlobal(h Home, s *Secrets) (*Global, error) {
	data, err := os.ReadFile(h.Config())
	if err != nil {
		return nil, fmt.Errorf("global config: %w", err)
	}
	return ParseGlobal(h, h.Config(), data, s)
}

// ParseGlobal decodes and validates global config data.
func ParseGlobal(h Home, file string, data []byte, s *Secrets) (*Global, error) {
	g := &Global{}
	if err := decode(file, data, s, g); err != nil {
		return nil, err
	}
	if g.Concurrency == 0 {
		g.Concurrency = 1
	}
	fe := func(field, msg string) error { return &FieldError{File: file, Field: field, Err: errors.New(msg)} }
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
