package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

var spec = Spec{User: "deploy", UserHome: "/home/deploy", Binary: "/home/deploy/.local/bin/gosha",
	Home: "/home/deploy/.gosha", Path: "/home/deploy/.local/bin:/usr/bin:/bin"}

// fakeRun records commands; fail makes the commands it matches fail.
type fakeRun struct {
	calls []string
	fail  func(cmd string) bool
	out   string
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, cmd)
	if f.fail != nil && f.fail(cmd) {
		return nil, errors.New("exit status 1")
	}
	return []byte(f.out), nil
}

func TestSystemdUnit(t *testing.T) {
	got, err := (&Systemd{}).Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"User=deploy\n",
		`Environment="HOME=/home/deploy"` + "\n",
		`Environment="PATH=/home/deploy/.local/bin:/usr/bin:/bin"` + "\n",
		`ExecStart="/home/deploy/.local/bin/gosha" daemon --home "/home/deploy/.gosha"` + "\n",
		"Restart=on-failure\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("unit lacks %q:\n%s", want, got)
		}
	}
	odd := spec
	odd.Path = `/a b:/c%d:/e"f$g`
	got, _ = (&Systemd{}).Render(odd)
	if !strings.Contains(string(got), `Environment="PATH=/a b:/c%%d:/e\"f$$g"`) {
		t.Errorf("PATH not escaped:\n%s", got)
	}
}

func TestSystemdInstall(t *testing.T) {
	f := &fakeRun{}
	m := &Systemd{Run: f.run, Path: filepath.Join(t.TempDir(), "gosha.service")}
	if err := m.Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl daemon-reload", "systemctl enable gosha", "systemctl restart gosha"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q", f.calls)
	}
	if !Installed(m) {
		t.Error("unit not written")
	}
	f.calls = nil
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if Installed(m) || strings.Join(f.calls, "|") != "systemctl disable --now gosha|systemctl daemon-reload" {
		t.Errorf("uninstall: calls = %q, installed = %v", f.calls, Installed(m))
	}
}

func TestSystemdStatus(t *testing.T) {
	f := &fakeRun{out: "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=4242\nActiveEnterTimestamp=Sun 2026-10-18 09:00:00 UTC\n"}
	s, err := (&Systemd{Run: f.run}).Status(context.Background())
	if err != nil || s != "active (running) · pid 4242 · since Sun 2026-10-18 09:00:00 UTC" {
		t.Errorf("status = %q, %v", s, err)
	}
}

func TestLaunchdPlist(t *testing.T) {
	odd := spec
	odd.Home = "/Users/a&b/.gosha"
	got, err := (&Launchd{}).Render(odd)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<key>UserName</key>\n\t<string>deploy</string>",
		"<string>/home/deploy/.local/bin/gosha</string>\n\t\t<string>daemon</string>\n\t\t<string>--home</string>\n\t\t<string>/Users/a&amp;b/.gosha</string>",
		"<key>PATH</key>\n\t\t<string>/home/deploy/.local/bin:/usr/bin:/bin</string>",
		"<key>KeepAlive</key>\n\t<true/>",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("plist lacks %q:\n%s", want, got)
		}
	}
}

func TestLaunchdInstallReplacesLoaded(t *testing.T) {
	f := &fakeRun{}
	m := &Launchd{Run: f.run, Path: filepath.Join(t.TempDir(), "dev.gosha.daemon.plist")}
	if err := m.Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	want := "launchctl print system/dev.gosha.daemon|launchctl bootout system/dev.gosha.daemon|launchctl bootstrap system " + m.Path
	if strings.Join(f.calls, "|") != want {
		t.Errorf("calls = %q", f.calls)
	}
}

func TestLaunchdStartWhenUnloaded(t *testing.T) {
	f := &fakeRun{fail: func(cmd string) bool { return strings.HasPrefix(cmd, "launchctl print") }}
	m := &Launchd{Run: f.run, Path: "/x.plist"}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "launchctl print system/dev.gosha.daemon|launchctl bootstrap system /x.plist|launchctl kickstart system/dev.gosha.daemon"
	if strings.Join(f.calls, "|") != want {
		t.Errorf("calls = %q", f.calls)
	}
	// Stopping an unloaded daemon is a no-op.
	f.calls = nil
	if err := m.Stop(context.Background()); err != nil || len(f.calls) != 1 {
		t.Errorf("stop: %v, calls = %q", err, f.calls)
	}
}

func TestResolveUserRefusesRoot(t *testing.T) {
	if _, err := ResolveUser("root"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("root: %v", err)
	}
}

func TestLoginPathTakesLastLine(t *testing.T) {
	f := &fakeRun{out: "Welcome!\n/usr/local/bin:/usr/bin"}
	cur, err := ResolveUser("")
	if err != nil {
		t.Skip(err)
	}
	p, err := LoginPath(context.Background(), f.run, cur, true)
	if err != nil || p != "/usr/local/bin:/usr/bin" || !strings.HasPrefix(f.calls[0], "su -l "+cur.Username+" -c ") {
		t.Errorf("path = %q, %v, calls = %q", p, err, f.calls)
	}
}

func TestQuote(t *testing.T) {
	if got := Quote("it's"); got != `'it'\''s'` {
		t.Errorf("Quote = %s", got)
	}
}
