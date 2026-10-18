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
			warns, err := validateRepo(dir)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, warn := range warns {
				fmt.Fprintf(w, "! %s\n", warn)
			}
			fmt.Fprintf(w, "✓ %s\n", filepath.Join(dir, config.ProjectFile))
			return nil
		},
	}
}

// validateRepo checks the config in a local checkout of a project's repo,
// and that the files it refers to exist.
func validateRepo(dir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, config.ProjectFile))
	if err != nil {
		return nil, err
	}
	cfg, warns, err := config.ParseProject(data, "")
	if err != nil {
		return nil, err
	}
	if wf := cfg.Deploy.Workflow; wf != "" {
		path := filepath.Join(".github", "workflows", wf)
		if _, err := os.Stat(filepath.Join(dir, path)); errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("deploy.workflow: %s doesn't exist", path)
		} else if err != nil {
			return nil, err
		}
	}
	return warns, nil
}
