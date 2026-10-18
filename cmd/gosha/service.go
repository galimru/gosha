package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/service"
)

// serviceRun runs service-manager commands; swapped in tests.
var serviceRun service.Runner = service.Exec

// hostManager is the host's service manager; swapped in tests.
var hostManager = func() (service.Manager, error) { return service.Host(serviceRun) }

func (a *app) serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage the Gosha service (systemd or launchd)",
		Long: "Register gosha daemon with the host's service manager (systemd on Linux, launchd on macOS)\n" +
			"and control it. install, uninstall, start, stop and restart need root (sudo); status doesn't.",
	}
	cmd.AddCommand(a.serviceInstallCmd(), a.serviceUninstallCmd(),
		a.serviceCtlCmd("start", "Start the service", service.Manager.Start),
		a.serviceCtlCmd("stop", "Stop the service; in-flight runs stay resumable", service.Manager.Stop),
		a.serviceCtlCmd("restart", "Restart the service; in-flight runs resume", service.Manager.Restart),
		a.serviceStatusCmd())
	return cmd
}

func needRoot(what string) error {
	if geteuid() != 0 {
		return fmt.Errorf("%s needs root: run it with sudo", what)
	}
	return nil
}

func (a *app) serviceInstallCmd() *cobra.Command {
	var userName, binary string
	var print bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the service and start it",
		Long: "Install gosha daemon as a system service that runs as a regular user, starts at boot and\n" +
			"restarts on failure. The user defaults to the one running sudo; root is refused. The unit\n" +
			"gets that user's login-shell PATH, so agents find its toolchains. Before writing anything,\n" +
			"gosha doctor must pass for that user. Re-running install replaces the unit and restarts it.\n" +
			"--print shows the unit without installing it (no root needed).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			root := geteuid() == 0
			if !print {
				if err := needRoot("service install"); err != nil {
					return err
				}
			}
			m, err := hostManager()
			if err != nil {
				return err
			}
			u, err := service.ResolveUser(userName)
			if err != nil {
				return err
			}
			if binary == "" {
				if binary, err = os.Executable(); err != nil {
					return err
				}
			}
			if binary, err = filepath.EvalSymlinks(binary); err != nil {
				return fmt.Errorf("--binary: %w", err)
			}
			if fi, err := os.Stat(binary); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
				return fmt.Errorf("--binary %s is not an executable file", binary)
			}
			home := a.homeFlag
			if home == "" {
				home = filepath.Join(u.HomeDir, ".gosha")
			}
			if home, err = filepath.Abs(home); err != nil {
				return err
			}
			path, err := service.LoginPath(ctx, serviceRun, u, root)
			if err != nil {
				return err
			}
			spec := service.Spec{User: u.Username, UserHome: u.HomeDir, Binary: binary, Home: home, Path: path}
			if print {
				unit, err := m.Render(spec)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "# %s\n%s", m.UnitPath(), unit)
				return nil
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "checking the host as %s (gosha doctor)…\n", u.Username)
			out, err := service.AsUser(ctx, serviceRun, u, root, service.Quote(binary)+" doctor --home "+service.Quote(home))
			cmd.ErrOrStderr().Write(out)
			if err != nil {
				return errors.New("gosha doctor failed for " + u.Username + ": fix the problems above, then run install again")
			}
			if err := m.Install(ctx, spec); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s (%s), running as %s with home %s\n", m.UnitPath(), m.Name(), u.Username, home)
			return nil
		},
	}
	cmd.Flags().StringVar(&userName, "user", "", "account to run as (default: the user running sudo, or the current user)")
	cmd.Flags().StringVar(&binary, "binary", "", "gosha binary the unit runs (default: this one)")
	cmd.Flags().BoolVar(&print, "print", false, "print the unit file instead of installing it")
	return cmd
}

func (a *app) serviceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the service and remove it",
		Long:  "Stop the service and remove its unit. Config, secrets and state in the Gosha home are left untouched.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := needRoot("service uninstall"); err != nil {
				return err
			}
			m, err := hostManager()
			if err != nil {
				return err
			}
			if err := m.Uninstall(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", m.UnitPath())
			return nil
		},
	}
}

func (a *app) serviceCtlCmd(verb, short string, do func(service.Manager, context.Context) error) *cobra.Command {
	return &cobra.Command{
		Use:   verb,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := needRoot("service " + verb); err != nil {
				return err
			}
			m, err := hostManager()
			if err != nil {
				return err
			}
			if !service.Installed(m) {
				return errors.New("the service isn't installed: run `sudo gosha service install`")
			}
			return do(m, cmd.Context())
		},
	}
}

func (a *app) serviceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the service manager's view of the service",
		Long:  "Show the service's state, PID and uptime as the service manager sees it. For what Gosha is doing, use gosha status.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := hostManager()
			if err != nil {
				return err
			}
			s, err := m.Status(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s): %s\n", m.UnitPath(), m.Name(), s)
			return nil
		},
	}
}
