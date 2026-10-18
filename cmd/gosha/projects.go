package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/gitx"
	"github.com/galimru/gosha/internal/store"
)

func (a *app) addCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <repo-url>",
		Short: "Register a project: bare-clone its repo and validate .gosha/config.yaml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			env, err := a.env(ctx)
			if err != nil {
				return err
			}
			repo, err := gitx.ParseGitHubURL(args[0])
			if err != nil {
				return err
			}
			inst, err := env.App.Installation(ctx, repo)
			if err != nil {
				return err
			}
			tok, err := inst.Token(ctx)
			if err != nil {
				return err
			}

			if err := os.MkdirAll(a.home.ProjectsDir(), 0o700); err != nil {
				return err
			}
			// Clone next to the projects so the final rename stays on one
			// filesystem; dot-directories aren't listed as projects.
			tmp, err := os.MkdirTemp(a.home.ProjectsDir(), ".add-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp)
			fmt.Fprintf(cmd.ErrOrStderr(), "cloning %s…\n", repo)
			r, err := gitx.CloneBare(ctx, repo.URL(), filepath.Join(tmp, "repo"), tok)
			if err != nil {
				return err
			}
			base, err := r.DefaultBranch(ctx)
			if err != nil {
				return err
			}
			commit, err := r.RevParse(ctx, "origin/"+base)
			if err != nil {
				return err
			}
			data, err := r.Show(ctx, commit, config.ProjectFile)
			if errors.Is(err, gitx.ErrNotFound) {
				return fmt.Errorf("%s has no %s on %s: run `gosha init` in a checkout and commit it", repo, config.ProjectFile, base)
			}
			if err != nil {
				return err
			}
			cfg, warns, err := config.ParseProject(data, commit)
			if err != nil {
				return err
			}
			pp := a.home.Project(cfg.Name)
			if _, err := os.Stat(pp.Repo()); err == nil {
				return fmt.Errorf("project %q is already registered (%s)", cfg.Name, pp.Dir)
			}
			if err := os.MkdirAll(pp.Dir, 0o700); err != nil {
				return err
			}
			if err := os.Rename(r.Dir, pp.Repo()); err != nil {
				return err
			}
			if _, err := env.Store.SetProjectState(ctx, store.ProjectState{Project: cfg.Name, Status: store.StatusActive, Commit: commit}); err != nil {
				return err
			}
			for _, w := range warns {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added %s (%s, %s @ %.7s, Linear team %s)\n", cfg.Name, repo, base, commit, cfg.Linear.Team)
			return nil
		},
	}
}

func (a *app) removeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <project>",
		Short: "Unregister a project and delete its directory (clone, worktrees, context, transcripts)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := a.checkProject(name); err != nil {
				return err
			}
			pp := a.home.Project(name)
			if !yes {
				fmt.Fprintf(cmd.ErrOrStderr(), "This deletes %s. Type the project name to confirm: ", pp.Dir)
				line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if strings.TrimSpace(line) != name {
					return errors.New("not confirmed")
				}
			}
			if err := os.RemoveAll(pp.Dir); err != nil {
				return err
			}
			st, err := store.Open(cmd.Context(), a.home.DB())
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.DeleteProjectState(cmd.Context(), name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func (a *app) listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			names, err := a.home.Projects()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no projects; add one with `gosha add <repo-url>`")
				return nil
			}
			st, err := store.Open(ctx, a.home.DB())
			if err != nil {
				return err
			}
			defer st.Close()
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "PROJECT\tREPO\tBRANCH\tSTATUS")
			for _, n := range names {
				r := gitx.Open(a.home.Project(n).Repo())
				origin, _ := r.OriginURL(ctx)
				if gr, err := gitx.ParseGitHubURL(origin); err == nil {
					origin = gr.String()
				}
				branch, _ := r.DefaultBranch(ctx)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", n, origin, branch, statusText(ctx, st, n))
			}
			return w.Flush()
		},
	}
}
