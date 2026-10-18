package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/gitx"
)

type check struct {
	level string // ok, warn, fail
	msg   string
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the host: required tools, not root, file permissions, global config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := a.doctor(cmd.Context())
			failed := 0
			for _, c := range checks {
				mark := map[string]string{"ok": "✓", "warn": "!", "fail": "✗"}[c.level]
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", mark, a.redact(c.msg))
				if c.level == "fail" {
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d check(s) failed", failed)
			}
			return nil
		},
	}
}

func (a *app) doctor(ctx context.Context) []check {
	var cs []check
	add := func(level, format string, args ...any) { cs = append(cs, check{level, fmt.Sprintf(format, args...)}) }

	if err := refuseRoot(); err != nil {
		add("fail", "running as root: Gosha refuses root, use a regular user")
	} else {
		add("ok", "not running as root (uid %d)", geteuid())
	}

	for _, tool := range []string{"git", "claude"} {
		if v, err := toolVersion(ctx, tool); err != nil {
			add("fail", "%s: %v", tool, err)
		} else {
			add("ok", "%s: %s", tool, v)
		}
	}
	if major, minor, err := gitx.Version(ctx); err == nil {
		if want := gitx.MinVersion; major < want[0] || major == want[0] && minor < want[1] {
			add("fail", "git %d.%d is too old: Gosha needs %d.%d or newer (merge-tree --write-tree)", major, minor, want[0], want[1])
		}
	}
	if runtime.GOOS == "linux" {
		// The agent sandbox needs these on Linux (not yet verified, SPIKES.md open items).
		for _, tool := range []string{"bwrap", "socat"} {
			if p, err := exec.LookPath(tool); err != nil {
				add("warn", "%s not on PATH: Claude Code's sandbox needs it on Linux", tool)
			} else {
				add("ok", "%s: %s", tool, p)
			}
		}
	}

	fi, err := os.Stat(a.home.Dir())
	if err != nil {
		add("fail", "home %s: %v", a.home.Dir(), err)
		return cs
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		add("fail", "home %s has permissions %04o, run: chmod 700 %s", a.home.Dir(), perm, a.home.Dir())
	} else {
		add("ok", "home %s", a.home.Dir())
	}

	files := []string{a.home.Secrets()}
	names, _ := a.home.Projects()
	for _, n := range names {
		files = append(files, a.home.Project(n).Secrets())
	}
	for _, f := range files {
		if _, err := config.ReadEnvFile(f); err != nil {
			add("fail", "%v", err)
		} else if _, err := os.Stat(f); err == nil {
			add("ok", "%s", f)
		} else if errors.Is(err, fs.ErrNotExist) && f == a.home.Secrets() {
			add("warn", "%s doesn't exist: create it with `gosha secrets edit`", f)
		}
	}

	secrets, err := config.LoadSecrets(a.home, "", a.red)
	if err != nil {
		add("fail", "%v", err)
		return cs
	}
	g, err := config.LoadGlobal(a.home, secrets)
	if err != nil {
		add("fail", "%v", err)
		return cs
	}
	add("ok", "global config %s (concurrency %d)", a.home.Config(), g.Concurrency)
	if !g.Agent.SandboxEnabled() {
		add("warn", "agent.sandbox is false: agents can read Gosha's secrets; a separate OS user (--user) is the remaining protection")
	}
	return cs
}

func toolVersion(ctx context.Context, tool string) (string, error) {
	path, err := exec.LookPath(tool)
	if err != nil {
		return "", errors.New("not on PATH")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var outb bytes.Buffer
	c := exec.CommandContext(ctx, path, "--version")
	c.Stdout = &outb
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("%s --version: %w", path, err)
	}
	return strings.TrimSpace(strings.SplitN(outb.String(), "\n", 2)[0]) + " (" + path + ")", nil
}
