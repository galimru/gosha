package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/agent"
	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/pipeline"
)

// pollInterval is how often the foreground loop looks for Ready tickets.
const pollInterval = 30 * time.Second

func (a *app) runCmd() *cobra.Command {
	var dryRun, once bool
	var ticket string
	cmd := &cobra.Command{
		Use:   "run <project>",
		Short: "Run one project in the foreground",
		Long: "Run one project in the foreground, for development and prompt tuning: pick up Ready\n" +
			"tickets one at a time and take each to a reviewed pull request (M3). Ctrl-C stops the agent\n" +
			"and leaves the ticket In Progress; the next run continues it.\n\n" +
			"--once handles at most one ticket and exits.\n" +
			"--dry-run does a single pickup pass: it lists the Ready tickets, decides what would\n" +
			"be picked up and how, and reports it here and in Telegram. Nothing changes in Linear or GitHub.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseRoot(); err != nil {
				return err
			}
			name := args[0]
			if err := a.checkProject(name); err != nil {
				return err
			}
			ctx := cmd.Context()
			env, err := a.env(ctx)
			if err != nil {
				return err
			}
			if dryRun {
				report, err := env.DryRun(ctx, name, ticket)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), report)
				return nil
			}
			if _, err := toolVersion(ctx, "claude"); err != nil {
				return fmt.Errorf("claude: %w", err)
			}
			if !env.Global.Agent.SandboxEnabled() {
				a.log.Warn("agent.sandbox is false: agents can read Gosha's secrets")
			}
			if once {
				handled, err := env.Pass(ctx, name, ticket)
				if err == nil && !handled {
					fmt.Fprintln(cmd.OutOrStdout(), "nothing to pick up")
				}
				return err
			}
			return a.loop(ctx, env, name, ticket)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only report what would happen")
	cmd.Flags().BoolVar(&once, "once", false, "handle at most one ticket, then exit")
	cmd.Flags().StringVar(&ticket, "ticket", "", "consider only this ticket (e.g. NXT-12)")
	return cmd
}

// loop polls the project until ctx ends. A usage limit pauses it until the
// limit resets; any other error is logged and retried on the next poll.
func (a *app) loop(ctx context.Context, env *pipeline.Env, name, ticket string) error {
	a.log.Info("running", "project", name, "poll", pollInterval)
	for {
		handled, err := env.Pass(ctx, name, ticket)
		wait := pollInterval
		var le *agent.LimitError
		var pe *pipeline.PausedError
		switch {
		case ctx.Err() != nil:
			a.log.Info("stopped; an interrupted ticket continues on the next run", "project", name)
			return nil
		case errors.As(err, &le):
			wait = time.Until(le.ResetsAt)
			if le.ResetsAt.IsZero() || wait <= 0 {
				wait = 15 * time.Minute
			}
			a.log.Warn("usage limit, pausing", "project", name, "until", time.Now().Add(wait).Format(time.Kitchen))
		case errors.As(err, &pe):
			a.log.Warn("project paused", "project", name, "reason", a.redact(pe.Err.Error()))
		case err != nil:
			a.log.Error("pass failed", "project", name, "err", a.redact(err.Error()))
		case handled:
			continue
		}
		select {
		case <-ctx.Done():
			a.log.Info("stopped", "project", name)
			return nil
		case <-time.After(wait):
		}
		if le != nil {
			env.Notify.Send(notify.Event{Project: name, Text: "▶️ usage limit reset · resumed"})
		}
	}
}
