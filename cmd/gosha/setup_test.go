package main

import (
	"strings"
	"testing"
)

func TestSetupNeedsTerminal(t *testing.T) {
	out, err := runCLIIn(t, "tok\n", "setup")
	if err == nil || !strings.Contains(err.Error(), "interactive") || strings.Contains(out, "token") {
		t.Errorf("setup without a terminal: %v, %q", err, out)
	}
}
