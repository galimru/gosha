package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/notion"
)

// starterConfig is what gosha init writes: the required fields, and every
// optional one commented out with its default.
const starterConfig = `# Gosha reads this file from the default branch: commit and merge it there.
name: %s

linear:
  team: %s                     # the team's key, as in ticket ids (NXT-12)
  # states:                    # only if the team's state names differ
  #   ready: Ready
  #   in_progress: In Progress
  #   in_review: In Review
  #   blocked: Blocked

notion:
  root_page: %s                # page URL or id, shared with Gosha's integration

# github:
#   default_branch: ""         # empty: the repo's default branch
#   merge_method: squash       # squash, merge or rebase

# review:
#   max_rounds: 3

# checks:
#   required: []               # check names that must pass; empty: whatever runs must pass
#   start_grace: 3m
#   timeout: 30m
#   max_rounds: 3

# deploy:
#   workflow: deploy.yml       # a file in .github/workflows/
#   url: https://example.com   # shown in the Delivered comment
#   timeout: 20m

# agent:
#   timeout: 1h                # budget for all agent work in one pickup
#   model: ""                  # empty: Claude Code's default
`

var nameChars = regexp.MustCompile(`[^a-z0-9_-]+`)

func (a *app) initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [path]",
		Short: "Write a starter .gosha/config.yaml in a local checkout",
		Long: "Write a starter .gosha/config.yaml in a local checkout of the project's repo (default: the\n" +
			"current directory). It asks for the project name, the Linear team key and the Notion root page,\n" +
			"and comments out every optional field with its default. An existing config is never overwritten.\n" +
			"Commit it to the default branch, then run gosha add <repo-url> on the Gosha host.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			path := filepath.Join(abs, config.ProjectFile)
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}

			r, w := bufio.NewReader(cmd.InOrStdin()), cmd.ErrOrStderr()
			def := strings.Trim(nameChars.ReplaceAllString(strings.ToLower(filepath.Base(abs)), "-"), "-_")
			name, err := ask(r, w, "Project name", def, func(s string) error {
				if !config.ValidName(s) {
					return errors.New("must be lowercase letters, digits, '-' or '_'")
				}
				return nil
			})
			if err != nil {
				return err
			}
			team, err := ask(r, w, "Linear team key (e.g. NXT)", "", nil)
			if err != nil {
				return err
			}
			team = strings.ToUpper(team)
			notionPage, err := ask(r, w, "Notion root page (URL or id)", "", func(s string) error {
				if _, err := notion.ParsePageID(s); err != nil {
					return errors.New("must be a Notion page URL or id")
				}
				return nil
			})
			if err != nil {
				return err
			}

			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, fmt.Appendf(nil, starterConfig, name, team, notionPage), 0o644); err != nil {
				return err
			}
			if err := validateRepo(abs); err != nil {
				os.Remove(path)
				return fmt.Errorf("not written: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\ncommit it to the default branch, then run `gosha add <repo-url>` on the Gosha host\n", path)
			return nil
		},
	}
}

// ask prompts on w until r gives an answer that check accepts. An empty
// answer takes def, and is refused when def is empty. Running out of input
// is an error, so init can't run unattended.
func ask(r *bufio.Reader, w io.Writer, prompt, def string, check func(string) error) (string, error) {
	if def != "" {
		prompt += " [" + def + "]"
	}
	for {
		fmt.Fprintf(w, "%s: ", prompt)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(w)
			return "", fmt.Errorf("init needs answers on stdin; write %s by hand to script it", config.ProjectFile)
		}
		s := strings.TrimSpace(line)
		if s == "" {
			s = def
		}
		if s == "" {
			fmt.Fprintln(w, "  required")
			continue
		}
		if check != nil {
			if err := check(s); err != nil {
				fmt.Fprintf(w, "  %v\n", err)
				continue
			}
		}
		return s, nil
	}
}

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect project config"}
	cmd.AddCommand(&cobra.Command{
		Use:   "show <project>",
		Short: "Print a project's effective config (the default branch's file plus defaults)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := a.checkProject(name); err != nil {
				return err
			}
			ctx := cmd.Context()
			env, err := a.env(ctx)
			if err != nil {
				return err
			}
			p, err := env.LoadProject(ctx, name)
			if err != nil {
				return err
			}
			cfg := *p.Config
			if cfg.GitHub.DefaultBranch == "" {
				cfg.GitHub.DefaultBranch = p.Base
			}
			out, err := config.Effective(&cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "# %s at %s@%.7s\n%s", config.ProjectFile, p.Base, p.Commit, out)
			return nil
		},
	})
	return cmd
}
