package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/release"
	"github.com/galimru/gosha/internal/service"
)

// releases is where upgrade looks; swapped in tests.
var releases = &release.Client{}

// executable is the running binary; swapped in tests.
var executable = os.Executable

func (a *app) upgradeCmd() *cobra.Command {
	var check bool
	var want string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Replace this binary with the latest release, and restart the service",
		Long: "Download the latest release (or --version) for this OS and architecture, verify its checksum,\n" +
			"and replace this binary with it atomically. If the service is installed and this runs as root,\n" +
			"the service is restarted and in-flight runs resume; otherwise the restart command is printed.\n" +
			"--check only reports whether a newer release exists.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			if version == "dev" {
				return errors.New("this is a dev build (built from source): upgrade it the way it was built, e.g. go install")
			}
			tag := want
			if tag == "" {
				latest, err := releases.Latest(ctx)
				if err != nil {
					return err
				}
				tag = latest
			}
			if check {
				switch release.Compare(version, tag) {
				case -1:
					fmt.Fprintf(out, "gosha %s; %s is available: run `gosha upgrade`\n", version, tag)
				default:
					fmt.Fprintf(out, "gosha %s is up to date (latest %s)\n", version, tag)
				}
				return nil
			}
			if want == "" && release.Compare(version, tag) >= 0 {
				fmt.Fprintf(out, "gosha %s is up to date (latest %s)\n", version, tag)
				return nil
			}
			exe, err := executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			if err := release.Writable(filepath.Dir(exe)); err != nil {
				return fmt.Errorf("can't replace %s (%v): run `sudo gosha upgrade`", exe, err)
			}
			bin, err := releases.Fetch(ctx, tag, runtime.GOOS, runtime.GOARCH)
			if err != nil {
				return err
			}
			if err := release.Replace(exe, bin); err != nil {
				return fmt.Errorf("replace %s: %w", exe, err)
			}
			fmt.Fprintf(out, "upgraded %s: %s → %s\n", exe, version, tag)
			m, err := hostManager()
			if err != nil || !service.Installed(m) {
				return nil
			}
			if geteuid() != 0 {
				fmt.Fprintln(out, "the service still runs the old version: run `sudo gosha service restart`")
				return nil
			}
			if err := m.Restart(ctx); err != nil {
				return fmt.Errorf("restart the service: %w", err)
			}
			fmt.Fprintln(out, "restarted the service; in-flight runs resume")
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().StringVar(&want, "version", "", "install this release (e.g. v0.3.1), older ones included")
	return cmd
}
