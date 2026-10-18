package main

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/store"
)

func statusText(ctx context.Context, st *store.Store, project string) string {
	ps, err := st.ProjectState(ctx, project)
	switch {
	case err != nil:
		return "unknown (" + err.Error() + ")"
	case ps == nil:
		return "not checked yet"
	case ps.Status == store.StatusPaused:
		return fmt.Sprintf("paused at %.7s: %s", ps.Commit, ps.Reason)
	}
	return fmt.Sprintf("active @ %.7s", ps.Commit)
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show each project's state and any runs in flight",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			names, err := a.home.Projects()
			if err != nil {
				return err
			}
			st, err := store.Open(ctx, a.home.DB())
			if err != nil {
				return err
			}
			defer st.Close()
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "PROJECT\tSTATE\tCHECKED")
			for _, n := range names {
				checked := "-"
				if ps, _ := st.ProjectState(ctx, n); ps != nil {
					checked = ps.UpdatedAt.Local().Format(time.DateTime)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", n, statusText(ctx, st, n), checked)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			runs, err := st.OpenRuns(ctx)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "\nno runs in flight")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout())
			w = tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "PROJECT\tTICKET\tMODE\tSTEP\tBRANCH\tSINCE")
			for _, r := range runs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Project, r.TicketID, r.Mode, r.Step, r.Branch, r.CreatedAt.Local().Format(time.DateTime))
			}
			return w.Flush()
		},
	}
}
