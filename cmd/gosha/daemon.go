package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run every registered project in the foreground",
		Long: "Run every project registered under the Gosha home, in the foreground, logging to\n" +
			"logs/gosha.log and stderr. This is what the service runs; any service manager can run it.\n" +
			"Projects added or removed while it runs are picked up within a minute.\n" +
			"SIGTERM stops the agents and leaves their tickets In Progress; the next start continues them.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := refuseRoot(); err != nil {
				return err
			}
			ctx := cmd.Context()
			env, err := a.env(ctx)
			if err != nil {
				return err
			}
			if _, err := toolVersion(ctx, "claude"); err != nil {
				return fmt.Errorf("claude: %w", err)
			}
			if !env.Global.Agent.SandboxEnabled() {
				a.log.Warn("agent.sandbox is false: agents run in auto mode without the sandbox and can read Gosha's secrets")
			}
			return a.daemon(env).Run(ctx)
		},
	}
}
