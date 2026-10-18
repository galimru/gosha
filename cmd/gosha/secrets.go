package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/config"
)

const secretsTemplate = `# Gosha secrets: KEY=value, one per line. Keep this file chmod 600.
# Config files reference these as ${KEY} (DESIGN §5.4).
LINEAR_CLIENT_ID=
LINEAR_CLIENT_SECRET=
NOTION_TOKEN=
TELEGRAM_BOT_TOKEN=
CLAUDE_CODE_OAUTH_TOKEN=
`

const projectSecretsTemplate = `# Per-project overrides of ~/.gosha/secrets.env: KEY=value, one per line.
`

func (a *app) secretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage secrets.env",
	}
	var project string
	edit := &cobra.Command{
		Use:   "edit",
		Short: "Open secrets.env in $EDITOR (created with mode 600 if missing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, tmpl := a.home.Secrets(), secretsTemplate
			if project != "" {
				if err := a.checkProject(project); err != nil {
					return err
				}
				path, tmpl = a.home.Project(project).Secrets(), projectSecretsTemplate
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			switch {
			case err == nil:
				_, err = f.WriteString(tmpl)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
				if err != nil {
					return err
				}
			case !errors.Is(err, fs.ErrExist):
				return err
			}
			if err := os.Chmod(path, 0o600); err != nil {
				return err
			}

			editor := os.Getenv("VISUAL")
			if editor == "" {
				editor = os.Getenv("EDITOR")
			}
			if editor == "" {
				editor = "vi"
			}
			// $EDITOR may carry arguments ("code --wait").
			parts := strings.Fields(editor)
			ed := exec.CommandContext(cmd.Context(), parts[0], append(parts[1:], path)...)
			ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := ed.Run(); err != nil {
				return fmt.Errorf("%s: %w", editor, err)
			}

			m, err := config.ReadEnvFile(path)
			if err != nil {
				return err
			}
			var set, empty []string
			for k, v := range m {
				if v == "" {
					empty = append(empty, k)
				} else {
					set = append(set, k)
				}
			}
			sort.Strings(set)
			sort.Strings(empty)
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %d set (%s)\n", path, len(set), strings.Join(set, ", "))
			if len(empty) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "empty: %s\n", strings.Join(empty, ", "))
			}
			return nil
		},
	}
	edit.Flags().StringVar(&project, "project", "", "edit this project's overrides instead of the shared file")
	cmd.AddCommand(edit)
	return cmd
}
