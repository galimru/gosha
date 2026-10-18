package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"
)

// SystemdUnit is where the systemd unit is installed.
const SystemdUnit = "/etc/systemd/system/gosha.service"

const systemdName = "gosha"

// Systemd manages the gosha systemd unit.
type Systemd struct {
	Run  Runner
	Path string
}

var systemdTmpl = template.Must(template.New("unit").Funcs(funcs).Parse(`[Unit]
Description=Gosha: Linear tickets to merged pull requests
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User={{.User}}
Environment={{sd (print "HOME=" .UserHome)}}
Environment={{sd (print "PATH=" .Path)}}
ExecStart={{sd .Binary}} daemon --home {{sd .Home}}
Restart=on-failure
RestartSec=10
# SIGTERM goes to gosha, which stops its agents and leaves runs resumable.
KillMode=mixed
TimeoutStopSec=60

[Install]
WantedBy=multi-user.target
`))

func (m *Systemd) Name() string                  { return "systemd" }
func (m *Systemd) UnitPath() string              { return m.Path }
func (m *Systemd) Render(s Spec) ([]byte, error) { return render(systemdTmpl, s) }

func (m *Systemd) Install(ctx context.Context, s Spec) error {
	unit, err := m.Render(s)
	if err != nil {
		return err
	}
	if err := writeUnit(m.Path, unit); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", systemdName}, {"restart", systemdName}} {
		if _, err := m.Run(ctx, "systemctl", args...); err != nil {
			return err
		}
	}
	return nil
}

func (m *Systemd) Uninstall(ctx context.Context) error {
	if _, err := m.Run(ctx, "systemctl", "disable", "--now", systemdName); err != nil {
		return err
	}
	if err := os.Remove(m.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := m.Run(ctx, "systemctl", "daemon-reload")
	return err
}

func (m *Systemd) Start(ctx context.Context) error   { return m.ctl(ctx, "start") }
func (m *Systemd) Stop(ctx context.Context) error    { return m.ctl(ctx, "stop") }
func (m *Systemd) Restart(ctx context.Context) error { return m.ctl(ctx, "restart") }

func (m *Systemd) ctl(ctx context.Context, verb string) error {
	_, err := m.Run(ctx, "systemctl", verb, systemdName)
	return err
}

func (m *Systemd) Status(ctx context.Context) (string, error) {
	out, err := m.Run(ctx, "systemctl", "show", systemdName, "--property=LoadState,ActiveState,SubState,MainPID,ActiveEnterTimestamp")
	if err != nil {
		return "", err
	}
	p := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			p[k] = v
		}
	}
	if p["LoadState"] == "not-found" {
		return "not installed", nil
	}
	s := fmt.Sprintf("%s (%s)", p["ActiveState"], p["SubState"])
	if pid := p["MainPID"]; pid != "" && pid != "0" {
		s += " · pid " + pid
	}
	if since := p["ActiveEnterTimestamp"]; since != "" && p["ActiveState"] == "active" {
		s += " · since " + since
	}
	return s, nil
}
