package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/linear"
)

func (a *app) authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Check Gosha's identities",
	}
	var project string
	lin := &cobra.Command{
		Use:   "linear",
		Short: "Check the Linear app credentials and show the actor Gosha acts as",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project != "" {
				if err := a.checkProject(project); err != nil {
					return err
				}
			}
			secrets, err := config.LoadSecrets(a.home, project, a.red)
			if err != nil {
				return err
			}
			id, err := config.Expand("${LINEAR_CLIENT_ID}", secrets)
			if err != nil {
				return err
			}
			secret, err := config.Expand("${LINEAR_CLIENT_SECRET}", secrets)
			if err != nil {
				return err
			}
			v, err := linear.New(id, secret).Viewer(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Gosha acts on Linear as %s (%s, id %s)\n", v.Name, v.DisplayName, v.ID)
			return nil
		},
	}
	lin.Flags().StringVar(&project, "project", "", "use this project's secrets.env overrides")
	cmd.AddCommand(lin)
	return cmd
}
