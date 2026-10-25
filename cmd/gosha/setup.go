package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/prompt"
	"github.com/galimru/gosha/internal/setup"
)

// isTerminal is swapped in tests.
var isTerminal = prompt.IsTerminal

func (a *app) setupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Set up the host interactively: Claude, Linear, Notion, Telegram and the GitHub App",
		Long: "Set up this host interactively: it asks for the Claude, Linear, Notion and Telegram credentials\n" +
			"and creates (or checks) the GitHub App, checking each answer as it goes, and writes config.yaml,\n" +
			"secrets.env and github-app.pem in the Gosha home. Re-run it any time: Enter keeps a value that's\n" +
			"already set, and kept values are checked again. The files can also be written by hand.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := refuseRoot(); err != nil {
				return err
			}
			if !isTerminal(cmd.InOrStdin()) {
				return errors.New("setup is interactive: run it in a terminal, or write config.yaml and secrets.env by hand")
			}
			defer prompt.ExitOnInterrupt(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr())()
			s := &setup.Setup{Home: a.home, P: prompt.New(cmd.InOrStdin(), cmd.ErrOrStderr()), Red: a.red}
			return s.Run(cmd.Context())
		},
	}
}
