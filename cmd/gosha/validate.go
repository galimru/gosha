package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/pipeline"
)

func (a *app) validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [project]",
		Short: "Check the global config, credentials, and each project's config and Linear states",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			w := cmd.OutOrStdout()
			env, err := a.env(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "✓ global config %s\n", a.home.Config())
			if !env.Global.Agent.SandboxEnabled() {
				fmt.Fprintln(w, "! agent.sandbox is false: agents can read Gosha's secrets")
			}
			info, err := env.App.Info(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "✓ GitHub App %s (acts as %s)\n", info.Name, info.BotLogin())

			names := args
			if len(names) == 0 {
				if names, err = a.home.Projects(); err != nil {
					return err
				}
			}
			failed := 0
			for _, name := range names {
				if err := a.validateProject(cmd, env, name); err != nil {
					fmt.Fprintf(w, "✗ %s: %s\n", name, a.redact(err.Error()))
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d project(s) failed validation", failed, len(names))
			}
			return nil
		},
	}
}

func (a *app) validateProject(cmd *cobra.Command, env *pipeline.Env, name string) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()
	if err := a.checkProject(name); err != nil {
		return err
	}
	p, err := env.LoadProject(ctx, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ %s: config at %s@%.7s (%s)\n", name, p.Base, p.Commit, p.GitHub)
	for _, warn := range p.Warnings {
		fmt.Fprintf(w, "! %s: %s\n", name, warn)
	}
	lin, err := env.ConnectLinear(ctx, p)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ %s: Linear team %s (%s), states %q %q %q %q\n", name, lin.Team.Key, lin.Team.Name,
		lin.States.Ready.Name, lin.States.InProgress.Name, lin.States.InReview.Name, lin.States.Blocked.Name)
	branch, _ := p.Config.BranchName(config.SampleTicket)
	fmt.Fprintf(w, "✓ %s: branch pattern → %s\n", name, branch)
	fmt.Fprintf(w, "✓ %s: Notion token set (access is checked from M5)\n", name)
	return nil
}

// checkProject fails unless name is a registered project.
func (a *app) checkProject(name string) error {
	if _, err := os.Stat(a.home.Project(name).Repo()); err != nil {
		return fmt.Errorf("no project %q: see `gosha list`", name)
	}
	return nil
}
