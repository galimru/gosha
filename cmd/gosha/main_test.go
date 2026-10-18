package main

import (
	"strings"
	"testing"
)

func TestRefuseRoot(t *testing.T) {
	defer func(f func() int) { geteuid = f }(geteuid)
	geteuid = func() int { return 0 }
	if err := refuseRoot(); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("root not refused: %v", err)
	}
	geteuid = func() int { return 501 }
	if err := refuseRoot(); err != nil {
		t.Errorf("regular user refused: %v", err)
	}
}

func TestRunRefusesRoot(t *testing.T) {
	defer func(f func() int) { geteuid = f }(geteuid)
	geteuid = func() int { return 0 }
	a := &app{homeFlag: t.TempDir()}
	root := a.rootCmd()
	root.SetArgs([]string{"--home", a.homeFlag, "run", "demo", "--dry-run"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "refusing to run as root") {
		t.Errorf("run as root: %v", err)
	}
}
