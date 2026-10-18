// Package service registers gosha daemon with the host's service manager:
// systemd on Linux, launchd on macOS.
package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/template"
)

// Spec is what a unit runs.
type Spec struct {
	// User is the account the daemon runs as, and UserHome its home
	// directory (the unit's HOME).
	User, UserHome string
	// Binary is the absolute path of the gosha binary.
	Binary string
	// Home is the Gosha home directory (--home).
	Home string
	// Path is the unit's PATH, from the user's login shell.
	Path string
}

// Runner runs a service-manager command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Exec runs commands for real.
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return out, nil
}

// Manager is one host's service manager.
type Manager interface {
	// Name is "systemd" or "launchd".
	Name() string
	// UnitPath is where the unit file lives.
	UnitPath() string
	// Render returns the unit file for spec.
	Render(Spec) ([]byte, error)
	// Install writes the unit and (re)starts the service.
	Install(ctx context.Context, s Spec) error
	// Uninstall stops the service and removes the unit.
	Uninstall(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	// Status is the service manager's view: state, PID, uptime.
	Status(ctx context.Context) (string, error)
}

// ErrUnsupported means the host has no service manager Gosha supports.
var ErrUnsupported = errors.New("no supported service manager: Gosha supports systemd (Linux) and launchd (macOS). " +
	"Run `gosha daemon` under your own supervisor instead; `gosha service install --print` shows a unit to start from")

// systemdBooted is the standard check that systemd is the running init.
var systemdBooted = "/run/systemd/system"

// Host returns the service manager of this host.
func Host(run Runner) (Manager, error) {
	switch runtime.GOOS {
	case "darwin":
		return &Launchd{Run: run, Path: LaunchdPlist}, nil
	case "linux":
		if fi, err := os.Stat(systemdBooted); err != nil || !fi.IsDir() {
			return nil, ErrUnsupported
		}
		return &Systemd{Run: run, Path: SystemdUnit}, nil
	}
	return nil, ErrUnsupported
}

// Installed reports whether m's unit file exists.
func Installed(m Manager) bool {
	_, err := os.Stat(m.UnitPath())
	return err == nil
}

func writeUnit(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return os.Rename(tmp, path)
}

func render(t *template.Template, s Spec) ([]byte, error) {
	var b bytes.Buffer
	if err := t.Execute(&b, s); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

var funcs = template.FuncMap{
	"xml": func(s string) string {
		var b strings.Builder
		xml.EscapeText(&b, []byte(s))
		return b.String()
	},
	// systemd: quote a value for Environment= or ExecStart=, escaping
	// backslashes, quotes and specifiers.
	"sd": func(s string) string {
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
		return `"` + r.Replace(s) + `"`
	},
}
