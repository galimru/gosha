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

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/notion"
	"github.com/galimru/gosha/internal/pipeline"
)

type check struct {
	level string // ok, warn, fail
	msg   string
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [project]",
		Short: "Check the host: tools, permissions, global config, credentials, and each project's Linear team and Notion access",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			checks := a.doctor(cmd.Context(), args)
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

func (a *app) doctor(ctx context.Context, projects []string) []check {
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
		add("fail", "home %s: %v%s", a.home.Dir(), err, setupHint(err))
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
			add("warn", "%s doesn't exist: run `gosha setup`", f)
		}
	}

	g, err := config.LoadGlobal(a.home)
	if err != nil {
		add("fail", "%v%s", err, setupHint(err))
		return cs
	}
	add("ok", "global config %s (concurrency %d)", a.home.Config(), g.Concurrency)
	ws, _ := pipeline.DirSize(a.home.WorkspacesDir())
	var runs int64
	for _, n := range names {
		r, _ := pipeline.DirSize(a.home.Project(n).Runs())
		runs += r
	}
	add("ok", "disk: workspaces/ %s, projects/*/runs/ %s (kept for %s; `gosha prune` frees it now)",
		humanize.IBytes(uint64(ws)), humanize.IBytes(uint64(runs)), g.RetentionText)
	if !g.Agent.SandboxEnabled() {
		add("warn", "agent.sandbox is false: agents run in auto mode without the sandbox and can read Gosha's secrets; a separate OS user (--user) is the remaining protection")
	}

	env, err := a.env(ctx)
	if err != nil {
		add("fail", "%v", err)
		return cs
	}
	if info, err := env.App.Info(ctx); err != nil {
		add("fail", "GitHub App: %v", err)
	} else {
		add("ok", "GitHub App %s (acts as %s)", info.Name, info.BotLogin())
	}
	if len(projects) == 0 {
		projects = names
	}
	for _, name := range projects {
		if err := a.checkProject(name); err != nil {
			add("fail", "%v", err)
			continue
		}
		for _, c := range a.doctorProject(ctx, env, name) {
			cs = append(cs, check{c.level, name + ": " + c.msg})
		}
	}
	return cs
}

// doctorProject checks what a project's runs need: its config on the
// default branch, its secrets, its Linear team and states, and Notion
// access to its root page.
func (a *app) doctorProject(ctx context.Context, env *pipeline.Env, name string) []check {
	var cs []check
	add := func(level, format string, args ...any) { cs = append(cs, check{level, fmt.Sprintf(format, args...)}) }
	p, err := env.LoadProject(ctx, name)
	if err != nil {
		add("fail", "%v", err)
		return cs
	}
	add("ok", "config at %s@%.7s (%s)", p.Base, p.Commit, p.GitHub)
	lin, err := env.ConnectLinear(ctx, p)
	if err != nil {
		add("fail", "linear: %v", err)
	} else if v, err := lin.Client.Viewer(ctx); err != nil {
		add("fail", "linear: %v", err)
	} else {
		add("ok", "Linear team %s (%s) as %s, states %q %q %q %q", lin.Team.Key, lin.Team.Name, v.Name,
			lin.States.Ready.Name, lin.States.InProgress.Name, lin.States.InReview.Name, lin.States.Blocked.Name)
	}
	nc := notion.New(p.Creds.NotionToken)
	bot, err := nc.Me(ctx)
	if err != nil {
		add("fail", "notion: %v", err)
		return cs
	}
	root, _ := notion.ParsePageID(p.Config.Notion.RootPage)
	page, err := nc.Page(ctx, root)
	switch {
	case errors.Is(err, notion.ErrNoAccess):
		add("fail", "notion: can't read the root page: share it with the %q integration (%v)", bot.Name, err)
	case err != nil:
		add("fail", "notion: %v", err)
	default:
		add("ok", "Notion root page %q (as %s in %s)", page.Title(), bot.Name, bot.Bot.WorkspaceName)
	}
	return cs
}

// setupHint points at gosha setup when a file it writes is missing.
func setupHint(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return ": run `gosha setup`"
	}
	return ""
}

// checkProject fails unless name is a registered project.
func (a *app) checkProject(name string) error {
	if _, err := os.Stat(a.home.Project(name).Repo()); err != nil {
		return fmt.Errorf("no project %q: see `gosha list`", name)
	}
	return nil
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
