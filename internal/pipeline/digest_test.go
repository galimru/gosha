package pipeline

import (
	"testing"
	"time"

	"github.com/galimru/gosha/internal/store"
)

func TestDigestDue(t *testing.T) {
	loc := time.FixedZone("x", 3*3600)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 18, h, m, 0, 0, loc) }
	cases := []struct {
		now      time.Time
		sent     string
		due      bool
		nextHour int
		nextDay  int
	}{
		{at(8, 59), "2026-10-17", false, 9, 18}, // before the time: wait for it
		{at(9, 0), "2026-10-17", true, 9, 19},   // on time
		{at(15, 0), "2026-10-17", true, 9, 19},  // missed while down: sent late
		{at(15, 0), "2026-10-18", false, 9, 19}, // already sent before a restart
	}
	for _, c := range cases {
		due, next := digestDue(c.now, "09:00", c.sent)
		if due != c.due || next.Hour() != c.nextHour || next.Day() != c.nextDay {
			t.Errorf("digestDue(%v, %s) = %v, %v", c.now, c.sent, due, next)
		}
	}
}

func TestDigestText(t *testing.T) {
	counts := map[string]store.DayCounts{"nextdesk": {Delivered: 3, Blocked: 1}, "avisodesk": {Delivered: 1}}
	if got := digestText("09:00", []string{"nextdesk"}, counts, map[string]int{"nextdesk": 3}); got != "09:00 · yesterday: 3 delivered, 1 blocked · queue: 3 Ready" {
		t.Errorf("one project: %q", got)
	}
	got := digestText("09:00", []string{"avisodesk", "nextdesk"}, counts, map[string]int{"avisodesk": -1, "nextdesk": 2})
	want := "09:00 · yesterday: 4 delivered, 1 blocked · queue: 2+? Ready\n" +
		"avisodesk: 1 delivered, 0 blocked · queue: ? Ready\n" +
		"nextdesk: 3 delivered, 1 blocked · queue: 2 Ready"
	if got != want {
		t.Errorf("two projects:\n%s\nwant\n%s", got, want)
	}
}
