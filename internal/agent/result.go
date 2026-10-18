package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Implementer result statuses.
const (
	StatusDone    = "done"
	StatusBlocked = "blocked"
)

// ImplementerResult is the implementer's result file.
type ImplementerResult struct {
	Status       string   `json:"status"`
	Summary      string   `json:"summary"`
	Verification string   `json:"verification"`
	Assumptions  []string `json:"assumptions"`
	// LeftOut is what the ticket could be read to ask for but was
	// deliberately not done, for the Delivered comment.
	LeftOut       []string `json:"left_out"`
	BlockedReason string   `json:"blocked_reason"`
	Questions     []string `json:"questions"`
}

// ParseImplementer decodes and checks a result file. Unknown fields are
// errors, so a typo doesn't silently drop information.
func ParseImplementer(data []byte) (*ImplementerResult, error) {
	var r ImplementerResult
	if err := decodeStrict(data, &r); err != nil {
		return nil, err
	}
	switch r.Status {
	case StatusDone:
		if strings.TrimSpace(r.Summary) == "" {
			return nil, errors.New(`"summary" is required when status is "done"`)
		}
	case StatusBlocked:
		if strings.TrimSpace(r.BlockedReason) == "" {
			return nil, errors.New(`"blocked_reason" is required when status is "blocked"`)
		}
	default:
		return nil, fmt.Errorf(`"status" must be "done" or "blocked", got %q`, r.Status)
	}
	return &r, nil
}

// Reviewer verdicts and comment severities.
const (
	VerdictApprove        = "approve"
	VerdictRequestChanges = "request_changes"

	SeverityMust   = "must"
	SeverityShould = "should"
	SeverityNit    = "nit"
)

// ReviewComment is one reviewer finding. Line 0 is a comment on the file
// as a whole.
type ReviewComment struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Body     string `json:"body"`
}

// ReviewerResult is the reviewer's result file.
type ReviewerResult struct {
	Verdict  string          `json:"verdict"`
	Summary  string          `json:"summary"`
	Comments []ReviewComment `json:"comments"`
}

// Count returns how many comments have severity sev.
func (r *ReviewerResult) Count(sev string) int {
	n := 0
	for _, c := range r.Comments {
		if c.Severity == sev {
			n++
		}
	}
	return n
}

// ParseReviewer decodes and checks a reviewer result file.
func ParseReviewer(data []byte) (*ReviewerResult, error) {
	var r ReviewerResult
	if err := decodeStrict(data, &r); err != nil {
		return nil, err
	}
	if r.Verdict != VerdictApprove && r.Verdict != VerdictRequestChanges {
		return nil, fmt.Errorf(`"verdict" must be "approve" or "request_changes", got %q`, r.Verdict)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return nil, errors.New(`"summary" is required`)
	}
	for i, c := range r.Comments {
		switch {
		case strings.TrimSpace(c.Path) == "":
			return nil, fmt.Errorf(`comment %d: "path" is required`, i+1)
		case strings.TrimSpace(c.Body) == "":
			return nil, fmt.Errorf(`comment %d: "body" is required`, i+1)
		case c.Line < 0:
			return nil, fmt.Errorf(`comment %d: "line" must be a line number, or 0 for the whole file`, i+1)
		case c.Severity != SeverityMust && c.Severity != SeverityShould && c.Severity != SeverityNit:
			return nil, fmt.Errorf(`comment %d: "severity" must be "must", "should" or "nit", got %q`, i+1, c.Severity)
		}
	}
	return &r, nil
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

// ResultError means the agent didn't produce a valid result file, even
// after the retry.
type ResultError struct{ Err error }

func (e *ResultError) Error() string { return "agent result: " + e.Err.Error() }
func (e *ResultError) Unwrap() error { return e.Err }

// Implement runs the implementer and reads its result from resultFile.
func Implement(ctx context.Context, s Spec, resultFile string) (*ImplementerResult, *Outcome, error) {
	return runWithResult(ctx, s, resultFile, ParseImplementer)
}

// Review runs the reviewer and reads its result from resultFile.
func Review(ctx context.Context, s Spec, resultFile string) (*ReviewerResult, *Outcome, error) {
	return runWithResult(ctx, s, resultFile, ParseReviewer)
}

// runWithResult runs an agent and reads its result file. A missing or
// invalid result gets one retry in the same session, quoting the problem.
func runWithResult[T any](ctx context.Context, s Spec, resultFile string, parse func([]byte) (T, error)) (T, *Outcome, error) {
	var zero T
	os.Remove(resultFile)
	out, err := Run(ctx, s)
	if err != nil {
		return zero, out, err
	}
	res, perr := readResult(resultFile, parse)
	if perr == nil {
		return res, out, nil
	}
	if out.SessionID == "" {
		return zero, out, &ResultError{perr}
	}
	retry := s
	retry.Resume = out.SessionID
	retry.Prompt = fmt.Sprintf("Your result file %s is not valid: %v\n\n"+
		"Write it again now, as JSON with exactly the fields described earlier. Don't change any code.", resultFile, perr)
	out2, err := Run(ctx, retry)
	if out2 != nil {
		out2.Duration += out.Duration
		if out2.SessionID == "" {
			out2.SessionID = out.SessionID
		}
	}
	if err != nil {
		return zero, out2, err
	}
	res, perr = readResult(resultFile, parse)
	if perr != nil {
		return zero, out2, &ResultError{perr}
	}
	return res, out2, nil
}

func readResult[T any](path string, parse func([]byte) (T, error)) (T, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		var zero T
		return zero, errors.New("the file was not written")
	}
	if err != nil {
		var zero T
		return zero, err
	}
	return parse(data)
}

// ReadReview reads a reviewer result file written earlier, e.g. by the
// review step before a restart.
func ReadReview(path string) (*ReviewerResult, error) {
	return readResult(path, ParseReviewer)
}
