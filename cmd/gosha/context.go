package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/notion"
)

func (a *app) contextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Manage a project's context: its Notion pages mirrored as markdown",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "refresh <project>",
			Short: "Pull the latest from Notion into the project's mirror",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				if err := a.checkProject(args[0]); err != nil {
					return err
				}
				env, err := a.env(ctx)
				if err != nil {
					return err
				}
				p, err := env.LoadProject(ctx, args[0])
				if err != nil {
					return err
				}
				m, err := env.Mirror(p)
				if err != nil {
					return err
				}
				st, err := m.Refresh(ctx)
				if errors.Is(err, notion.ErrNoAccess) {
					return fmt.Errorf("%w: is the root page shared with the Notion integration?", err)
				}
				if err != nil {
					return err
				}
				info, err := m.Info()
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %d pages, %s\n%s\n", args[0], info.Pages, st, m.Dir)
				return nil
			},
		},
		&cobra.Command{
			Use:   "path <project>",
			Short: "Print where the project's mirror lives",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := a.checkProject(args[0]); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), a.home.Project(args[0]).Context())
				return nil
			},
		},
		&cobra.Command{
			Use:   "show <project>",
			Short: "Print INDEX.md, the page tree agents start from",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := a.checkProject(args[0]); err != nil {
					return err
				}
				m := &notion.Mirror{Dir: a.home.Project(args[0]).Context()}
				idx, err := m.Index()
				if errors.Is(err, notion.ErrNoMirror) {
					return fmt.Errorf("%w: run `gosha context refresh %s`", err, args[0])
				}
				if err != nil {
					return err
				}
				fmt.Fprint(cmd.OutOrStdout(), idx)
				return nil
			},
		},
	)
	return cmd
}
