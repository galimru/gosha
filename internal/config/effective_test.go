package config

import (
	"strings"
	"testing"
	"time"
)

func TestEffective(t *testing.T) {
	p, err := ParseProject([]byte("name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\nchecks:\n  required: [test, lint]\n  timeout: 1h30m\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := Effective(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		"name: demo\n",
		"  states:\n    ready: Ready\n    in_progress: In Progress\n",
		"  merge_method: squash\n",
		"checks:\n  required: [test, lint]\n  start_grace: 3m\n  timeout: 1h30m\n  max_rounds: 3\n",
		"agent:\n  timeout: 1h\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("effective config lacks %q:\n%s", want, got)
		}
	}
	// It parses back to the same config.
	again, err := ParseProject(out, "")
	if err != nil || again.Checks.Timeout != 90*time.Minute {
		t.Errorf("round trip: %+v, %v", again, err)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{30 * time.Minute: "30m", time.Hour: "1h", 90 * time.Minute: "1h30m", 45 * time.Second: "45s", 0: "0s"} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
