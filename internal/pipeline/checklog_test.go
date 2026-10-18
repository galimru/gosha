package pipeline

import (
	"strings"
	"testing"
)

const jobLog = `2026-10-18T10:00:00.0000000Z ##[group]Run actions/checkout@v4
2026-10-18T10:00:00.1000000Z with: x
2026-10-18T10:00:00.2000000Z ##[endgroup]
2026-10-18T10:00:01.0000000Z Checked out.
2026-10-18T10:00:02.0000000Z ##[group]Run go test ./...
2026-10-18T10:00:02.1000000Z go test ./...
2026-10-18T10:00:02.2000000Z ##[endgroup]
2026-10-18T10:00:03.0000000Z --- FAIL: TestLogin (0.00s)
2026-10-18T10:00:03.1000000Z     login_test.go:12: want ok
2026-10-18T10:00:03.2000000Z FAIL
2026-10-18T10:00:03.3000000Z ##[error]Process completed with exit code 1.
2026-10-18T10:00:04.0000000Z Post job cleanup.
2026-10-18T10:00:04.1000000Z ##[group]Run actions/checkout post
2026-10-18T10:00:04.2000000Z Cleaning up.
`

func TestFailingStep(t *testing.T) {
	got := failingStep(jobLog)
	want := "##[group]Run go test ./...\ngo test ./...\n##[endgroup]\n--- FAIL: TestLogin (0.00s)\n    login_test.go:12: want ok\nFAIL\n##[error]Process completed with exit code 1."
	if got != want {
		t.Errorf("failingStep =\n%s\nwant\n%s", got, want)
	}
	// No error marker: the tail, capped.
	long := strings.Repeat("line\n", maxLogLines+50) + "last"
	if got := failingStep(long); !strings.HasSuffix(got, "last") || strings.Count(got, "\n") != maxLogLines-1 {
		t.Errorf("tail = %d lines", strings.Count(got, "\n")+1)
	}
	if got := failingStep(strings.Repeat("x", 3*maxLogBytes)); len(got) > maxLogBytes {
		t.Errorf("not capped: %d bytes", len(got))
	}
}
