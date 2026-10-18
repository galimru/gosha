package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"
)

// ResolveUser picks the account the service runs as: name if given, else
// the user who ran sudo, else the current user. Root is refused: agents
// run commands derived from ticket text.
func ResolveUser(name string) (*user.User, error) {
	if name == "" {
		name = os.Getenv("SUDO_USER")
	}
	var u *user.User
	var err error
	if name == "" {
		u, err = user.Current()
	} else {
		u, err = user.Lookup(name)
	}
	if err != nil {
		return nil, fmt.Errorf("service user: %w", err)
	}
	if u.Uid == "0" {
		return nil, errors.New("refusing to install the service for root: agents execute commands derived from ticket text. " +
			"Pass --user with a regular account (e.g. create one with `adduser deploy`)")
	}
	return u, nil
}

// AsUser runs a shell command line as u: through `su -l` when running as
// root, so it gets u's login environment, otherwise in the current user's
// login shell (u must then be the current user).
func AsUser(ctx context.Context, run Runner, u *user.User, root bool, cmdline string) ([]byte, error) {
	if root {
		return run(ctx, "su", "-l", u.Username, "-c", cmdline)
	}
	cur, err := user.Current()
	if err != nil {
		return nil, err
	}
	if cur.Uid != u.Uid {
		return nil, fmt.Errorf("acting as %s needs root (run with sudo)", u.Username)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return run(ctx, shell, "-l", "-c", cmdline)
}

// LoginPath is u's PATH from its login shell, so the agents' toolchains are
// found under the service manager.
func LoginPath(ctx context.Context, run Runner, u *user.User, root bool) (string, error) {
	out, err := AsUser(ctx, run, u, root, `printf '%s' "$PATH"`)
	if err != nil {
		return "", fmt.Errorf("read %s's PATH from its login shell: %w", u.Username, err)
	}
	// A login shell may print a banner first; PATH is the last line.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	path := strings.TrimSpace(lines[len(lines)-1])
	if path == "" {
		return "", fmt.Errorf("%s's login shell has an empty PATH", u.Username)
	}
	return path, nil
}

// Quote quotes s for a POSIX shell.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
