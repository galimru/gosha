package pipeline

import (
	"regexp"
	"slices"
	"strings"
)

// Limits for a failing step's log excerpt in the fix prompt.
const (
	maxLogLines = 200
	maxLogBytes = 16 << 10
)

// logTime is the timestamp GitHub Actions puts at the start of every line.
var logTime = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z `)

// failingStep cuts the failing step's section out of an Actions job log.
// A job log ends with post-job cleanup, so its tail rarely shows the error.
// Each step starts with a `##[group]Run …` line; the failing step is the one
// holding the first `##[error]`. A log without an error marker falls back to
// its tail.
func failingStep(log string) string {
	lines := strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = logTime.ReplaceAllString(l, "")
	}
	start, end := 0, len(lines)
	if e := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "##[error]") }); e >= 0 {
		for start = e; start > 0 && !strings.HasPrefix(lines[start], "##[group]Run "); start-- {
		}
		for end = e; end < len(lines) && strings.HasPrefix(lines[end], "##[error]"); end++ {
		}
	}
	section := lines[start:end]
	if len(section) > maxLogLines {
		section = section[len(section)-maxLogLines:]
	}
	out := strings.TrimSpace(strings.Join(section, "\n"))
	if len(out) > maxLogBytes {
		out = out[len(out)-maxLogBytes:]
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return out
}
