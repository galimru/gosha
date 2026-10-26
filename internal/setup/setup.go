// Package setup is gosha setup: it asks for the host's credentials, checks
// each against its service, and writes secrets.env, config.yaml and the
// GitHub App key (DESIGN §11.7).
package setup

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/prompt"
)

// Setup holds what the steps share.
type Setup struct {
	Home config.Home
	P    *prompt.Prompter
	Red  *config.Redactor

	// Endpoints; empty means the real services (tests point them at fakes).
	LinearAPI, LinearToken string
	NotionAPI              string
	TelegramAPI            string
	GitHubAPI, GitHubWeb   string
	// Browse opens a URL in a browser; nil means the system's.
	Browse func(url string) error
	// Wait is how long each Telegram long poll waits; 0 means 30s.
	Wait time.Duration

	out     io.Writer
	secrets map[string]string
}

// Run goes through every step in order. Each step saves as soon as it
// passes, so stopping halfway keeps the finished ones.
func (s *Setup) Run(ctx context.Context) error {
	s.out = s.P.Out()
	if err := s.Home.EnsureDir(); err != nil {
		return err
	}
	var err error
	if s.secrets, err = config.ReadEnvFile(s.Home.Secrets()); err != nil {
		return err
	}
	for _, v := range s.secrets {
		s.Red.Add(v)
	}
	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"Claude", s.claude},
		{"Linear", s.linear},
		{"Notion", s.notion},
		{"Telegram", s.telegram},
		{"GitHub", s.github},
	} {
		fmt.Fprintf(s.out, "\n%s\n", step.name)
		if err := step.run(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	fmt.Fprintf(s.out, "\nwrote %s and %s\nnext: gosha doctor, then sudo gosha service install\n", s.Home.Config(), s.Home.Secrets())
	return nil
}

func (s *Setup) say(format string, args ...any) {
	fmt.Fprintf(s.out, "  "+format+"\n", args...)
}

func (s *Setup) ok(format string, args ...any) {
	fmt.Fprintf(s.out, "  ✓ "+format+"\n", args...)
}

func (s *Setup) warn(format string, args ...any) {
	fmt.Fprintf(s.out, "  ! "+format+"\n", args...)
}

// fail reports a failed check; the step then asks again.
func (s *Setup) fail(err error) {
	fmt.Fprintf(s.out, "  ✗ %s\n", s.Red.Redact(err.Error()))
}

// secret asks for a secret, showing whether one is already set.
func (s *Setup) secret(label, key string) (string, error) {
	v, err := s.P.Secret("  "+label, s.secrets[key])
	if err == nil {
		s.Red.Add(v)
	}
	return v, err
}

// save writes secrets to secrets.env, keeping its other lines.
func (s *Setup) save(kv ...string) error {
	for i := 0; i+1 < len(kv); i += 2 {
		if err := config.SetSecret(s.Home.Secrets(), kv[i], kv[i+1]); err != nil {
			return err
		}
		s.secrets[kv[i]] = kv[i+1]
	}
	return nil
}

// setGlobal writes one config.yaml field.
func (s *Setup) setGlobal(field, value string) error {
	return config.SetGlobal(s.Home.Config(), field, value)
}

func (s *Setup) claude(context.Context) error {
	s.say("Run `claude setup-token` in another terminal and paste the token it prints.")
	tok, err := s.secret("token", config.KeyClaudeToken)
	if err != nil {
		return err
	}
	// Not checked: that would cost usage. A bad token shows up on the
	// first agent pass.
	return s.save(config.KeyClaudeToken, tok)
}
