package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/pipeline"
	"github.com/galimru/gosha/internal/store"
)

func (a *app) pruneCmd() *cobra.Command {
	var dryRun bool
	var olderThan string
	cmd := &cobra.Command{
		Use:   "prune [project]",
		Short: "Remove idle workspaces and old run files now",
		Long: "Apply the cleanup rules the daemon applies every hour: remove the workspace of a Blocked run\n" +
			"idle longer than retention (a resume rebuilds it), workspaces left without a run, and a ticket's\n" +
			"runs/ files (transcripts, settings) once its last run finished longer than retention ago. Active\n" +
			"runs are never touched and no run is deleted from gosha.db. Safe to run while the daemon runs.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := ""
			if len(args) == 1 {
				project = args[0]
				if err := a.checkProject(project); err != nil {
					return err
				}
			}
			ctx := cmd.Context()
			var age = olderThan
			if age == "" {
				g, err := config.LoadGlobal(a.home)
				if err != nil {
					return err
				}
				age = g.RetentionText
			}
			d, err := config.ParseAge(age)
			if err != nil {
				return fmt.Errorf("--older-than: %w", err)
			}
			st, err := store.Open(ctx, a.home.DB(), version)
			if err != nil {
				return err
			}
			defer st.Close()
			pruned, err := pipeline.Prune(ctx, a.home, st, project, d, dryRun)
			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			var total int64
			for _, p := range pruned {
				total += p.Bytes
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Path, humanize.IBytes(uint64(p.Bytes)), p.Ticket, p.Why)
			}
			w.Flush()
			done, verb := "removed", "freed"
			if dryRun {
				done, verb = "would be removed", "would free"
			}
			fmt.Fprintf(out, "%d %s (idle longer than %s), %s %s\n", len(pruned), done, age, verb, humanize.IBytes(uint64(total)))
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be removed without removing it")
	cmd.Flags().StringVar(&olderThan, "older-than", "", "idle age to remove at, e.g. 7d (default: retention)")
	return cmd
}
