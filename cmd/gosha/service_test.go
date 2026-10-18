package main

import (
	"strings"
	"testing"
)

func TestServiceNeedsRoot(t *testing.T) {
	defer func(f func() int) { geteuid = f }(geteuid)
	geteuid = func() int { return 501 }
	for _, verb := range []string{"install", "uninstall", "start", "stop", "restart"} {
		a := &app{}
		root := a.rootCmd()
		root.SetArgs([]string{"--home", t.TempDir(), "service", verb})
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "needs root") {
			t.Errorf("service %s as a regular user: %v", verb, err)
		}
	}
}
