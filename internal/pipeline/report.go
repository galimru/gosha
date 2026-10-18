package pipeline

import (
	"fmt"
	"strings"

	"github.com/galimru/gosha/internal/linear"
)

// Report renders a dry-run plan for the terminal and Telegram.
func Report(commit string, ds []Decision) string {
	head := fmt.Sprintf("dry-run @ %.7s", commit)
	if len(ds) == 0 {
		return head + " · nothing Ready"
	}
	lines := []string{fmt.Sprintf("%s · %d Ready", head, len(ds))}
	var queued, skipped []string
	for _, d := range ds {
		is := d.Issue
		switch d.Action {
		case Pickup:
			lines = append(lines, fmt.Sprintf("▶ would pick up %s %s (%s) · %s · %s",
				is.Identifier, is.Title, linear.PriorityName(is.Priority), d.Mode, d.Branch))
		case Queued:
			queued = append(queued, fmt.Sprintf("%s (%s)", is.Identifier, linear.PriorityName(is.Priority)))
		case Skipped:
			skipped = append(skipped, fmt.Sprintf("skipped: %s · %s", is.Identifier, d.Reason))
		}
	}
	if len(queued) > 0 {
		lines = append(lines, "queued: "+strings.Join(queued, ", "))
	}
	return strings.Join(append(lines, skipped...), "\n")
}
