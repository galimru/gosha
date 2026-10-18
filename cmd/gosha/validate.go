package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/config"
)

func (a *app) validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [path]",
		Short: "Check a repo's .gosha/config.yaml offline (no secrets or network needed, so it can run in CI)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			if err := validateRepo(dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s\n", filepath.Join(dir, config.ProjectFile))
			return nil
		},
	}
}

// validateRepo checks the config in a local checkout of a project's repo,
// and that the files it refers to exist.
func validateRepo(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, config.ProjectFile))
	if err != nil {
		return err
	}
	cfg, err := config.ParseProject(data, "")
	if err != nil {
		return err
	}
	if wf := cfg.Deploy.Workflow; wf != "" {
		path := filepath.Join(".github", "workflows", wf)
		if _, err := os.Stat(filepath.Join(dir, path)); errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("deploy.workflow: %s doesn't exist", path)
		} else if err != nil {
			return err
		}
	}
	return nil
}
