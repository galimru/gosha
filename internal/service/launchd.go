package service

import (
	"context"
	"errors"
	"os"
	"regexp"
	"text/template"
)

// LaunchdPlist is where the launchd daemon is installed.
const LaunchdPlist = "/Library/LaunchDaemons/dev.gosha.daemon.plist"

const (
	launchdLabel  = "dev.gosha.daemon"
	launchdTarget = "system/" + launchdLabel
)

// Launchd manages the gosha launchd daemon.
type Launchd struct {
	Run  Runner
	Path string
}

var launchdTmpl = template.Must(template.New("plist").Funcs(funcs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>dev.gosha.daemon</string>
	<key>UserName</key>
	<string>{{xml .User}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{xml .Binary}}</string>
		<string>daemon</string>
		<string>--home</string>
		<string>{{xml .Home}}</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		<string>{{xml .UserHome}}</string>
		<key>PATH</key>
		<string>{{xml .Path}}</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ExitTimeOut</key>
	<integer>60</integer>
</dict>
</plist>
`))

func (m *Launchd) Name() string                  { return "launchd" }
func (m *Launchd) UnitPath() string              { return m.Path }
func (m *Launchd) Render(s Spec) ([]byte, error) { return render(launchdTmpl, s) }

// loaded reports whether the daemon is loaded into launchd.
func (m *Launchd) loaded(ctx context.Context) bool {
	_, err := m.Run(ctx, "launchctl", "print", launchdTarget)
	return err == nil
}

func (m *Launchd) Install(ctx context.Context, s Spec) error {
	plist, err := m.Render(s)
	if err != nil {
		return err
	}
	if m.loaded(ctx) {
		if _, err := m.Run(ctx, "launchctl", "bootout", launchdTarget); err != nil {
			return err
		}
	}
	if err := writeUnit(m.Path, plist); err != nil {
		return err
	}
	_, err = m.Run(ctx, "launchctl", "bootstrap", "system", m.Path)
	return err
}

func (m *Launchd) Uninstall(ctx context.Context) error {
	if m.loaded(ctx) {
		if _, err := m.Run(ctx, "launchctl", "bootout", launchdTarget); err != nil {
			return err
		}
	}
	if err := os.Remove(m.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *Launchd) Start(ctx context.Context) error {
	if !m.loaded(ctx) {
		if _, err := m.Run(ctx, "launchctl", "bootstrap", "system", m.Path); err != nil {
			return err
		}
	}
	_, err := m.Run(ctx, "launchctl", "kickstart", launchdTarget)
	return err
}

// Stop unloads the daemon, so KeepAlive doesn't restart it until Start.
func (m *Launchd) Stop(ctx context.Context) error {
	if !m.loaded(ctx) {
		return nil
	}
	_, err := m.Run(ctx, "launchctl", "bootout", launchdTarget)
	return err
}

func (m *Launchd) Restart(ctx context.Context) error {
	if !m.loaded(ctx) {
		return m.Start(ctx)
	}
	_, err := m.Run(ctx, "launchctl", "kickstart", "-k", launchdTarget)
	return err
}

var (
	launchdState = regexp.MustCompile(`(?m)^\s*state = (.+)$`)
	launchdPID   = regexp.MustCompile(`(?m)^\s*pid = (\d+)$`)
)

func (m *Launchd) Status(ctx context.Context) (string, error) {
	out, err := m.Run(ctx, "launchctl", "print", launchdTarget)
	if err != nil {
		if _, serr := os.Stat(m.Path); serr != nil {
			return "not installed", nil
		}
		return "not loaded (stopped)", nil
	}
	s := "unknown"
	if mm := launchdState.FindSubmatch(out); mm != nil {
		s = string(mm[1])
	}
	if mm := launchdPID.FindSubmatch(out); mm != nil {
		s += " · pid " + string(mm[1])
	}
	return s, nil
}
