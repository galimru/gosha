package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) runCmd() *cobra.Command {
	var dryRun bool
	var ticket string
	cmd := &cobra.Command{
		Use:   "run <project>",
		Short: "Run one project in the foreground (M1: --dry-run only)",
		Long: "Run one project in the foreground, for development and prompt tuning.\n\n" +
			"--dry-run does a single pickup pass: it lists the Ready tickets, decides what would\n" +
			"be picked up and how, and reports it here and in Telegram. Nothing changes in Linear or GitHub.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseRoot(); err != nil {
				return err
			}
			if !dryRun {
				return errors.New("only --dry-run is available so far; running tickets arrives in M2 (docs/ROADMAP.md)")
			}
			name := args[0]
			if err := a.checkProject(name); err != nil {
				return err
			}
			env, err := a.env(cmd.Context())
			if err != nil {
				return err
			}
			report, err := env.DryRun(cmd.Context(), name, ticket)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only report what would happen")
	cmd.Flags().StringVar(&ticket, "ticket", "", "consider only this ticket (e.g. NXT-12)")
	return cmd
}
