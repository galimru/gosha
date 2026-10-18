package main

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
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
			"tickets one at a time and take each to a merged pull request with green checks (M4).\n" +
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
			if once {
				handled, err := a.pass(ctx, env, name, ticket)
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
		handled, err := a.pass(ctx, env, name, ticket)
		var crash *crashError
		if errors.As(err, &crash) {
			return err
		}
		if url := env.Global.HeartbeatURL; url != "" && ctx.Err() == nil {
			if err := notify.Ping(ctx, url); err != nil {
				a.log.Warn("heartbeat", "err", a.redact(err.Error()))
			}
		}
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

// crashError is a pass that panicked.
type crashError struct {
	panic any
	stack []byte
}

func (e *crashError) Error() string { return fmt.Sprintf("crashed: %v\n%s", e.panic, e.stack) }

// pass runs one pickup pass. A panic becomes a crashError and a Telegram
// alert, so the process can exit cleanly after the alert is sent.
func (a *app) pass(ctx context.Context, env *pipeline.Env, name, ticket string) (handled bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &crashError{panic: r, stack: debug.Stack()}
			a.log.Error("crashed", "project", name, "panic", a.redact(fmt.Sprint(r)))
			env.Notify.Send(notify.Event{Project: name, Text: fmt.Sprintf("💥 crashed: %v", r)})
		}
	}()
	return env.Pass(ctx, name, ticket)
}
