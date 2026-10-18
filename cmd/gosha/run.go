package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/daemon"
	"github.com/galimru/gosha/internal/pipeline"
)

func (a *app) runCmd() *cobra.Command {
	var dryRun, once bool
	var ticket string
	cmd := &cobra.Command{
		Use:   "run <project>",
		Short: "Run one project in the foreground",
		Long: "Run one project in the foreground, for development and prompt tuning: pick up Ready\n" +
			"tickets one at a time and take each through to delivery. The daemon must not be running\n" +
			"this project at the same time.\n" +
			"Ctrl-C stops the agent and leaves the ticket In Progress; the next run continues it.\n\n" +
			"--once handles at most one ticket and exits.\n" +
			"--dry-run does a single pickup pass: it lists the Ready tickets, decides what would\n" +
			"be picked up and how, and prints it. Nothing changes in Linear or GitHub, and nothing goes to Telegram.",
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
				a.log.Warn("agent.sandbox is false: agents run in auto mode without the sandbox and can read Gosha's secrets")
			}
			d := a.daemon(env)
			if once {
				unlock, err := daemon.LockProject(a.home, name)
				if err != nil {
					return err
				}
				defer unlock()
				handled, err := d.SafePass(ctx, name, ticket)
				if err == nil && !handled {
					fmt.Fprintln(cmd.OutOrStdout(), "nothing to pick up")
				}
				return err
			}
			go d.Heartbeat(ctx)
			return d.Supervise(ctx, name, ticket)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only report what would happen")
	cmd.Flags().BoolVar(&once, "once", false, "handle at most one ticket, then exit")
	cmd.Flags().StringVar(&ticket, "ticket", "", "consider only this ticket (e.g. NXT-12)")
	return cmd
}

// daemon wires the pipeline into the daemon. Its Jobs run only under
// gosha daemon, not gosha run.
func (a *app) daemon(env *pipeline.Env) *daemon.Daemon {
	return &daemon.Daemon{Home: a.home, Pass: env.Pass, Notify: env.Notify, Log: a.log,
		Redact: a.redact, HeartbeatURL: env.Global.HeartbeatURL, Limit: env.Limit,
		Jobs: []func(context.Context){env.DigestLoop, env.PruneLoop}}
}
