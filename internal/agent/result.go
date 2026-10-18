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

// ImplementerResult is the implementer's result file (DESIGN §7).
type ImplementerResult struct {
	Status        string   `json:"status"`
	Summary       string   `json:"summary"`
	Verification  string   `json:"verification"`
	Assumptions   []string `json:"assumptions"`
	BlockedReason string   `json:"blocked_reason"`
	Questions     []string `json:"questions"`
}

// ParseImplementer decodes and checks a result file. Unknown fields are
// errors, so a typo doesn't silently drop information.
func ParseImplementer(data []byte) (*ImplementerResult, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r ImplementerResult
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
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

// ResultError means the agent didn't produce a valid result file, even
// after the retry.
type ResultError struct{ Err error }

func (e *ResultError) Error() string { return "agent result: " + e.Err.Error() }
func (e *ResultError) Unwrap() error { return e.Err }

// Implement runs the implementer and reads its result from resultFile. A
// missing or invalid result gets one retry in the same session, quoting
// the problem (DESIGN §7).
func Implement(ctx context.Context, s Spec, resultFile string) (*ImplementerResult, *Outcome, error) {
	os.Remove(resultFile)
	out, err := Run(ctx, s)
	if err != nil {
		return nil, out, err
	}
	res, perr := readResult(resultFile)
	if perr == nil {
		return res, out, nil
	}
	if out.SessionID == "" {
		return nil, out, &ResultError{perr}
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
		return nil, out2, err
	}
	res, perr = readResult(resultFile)
	if perr != nil {
		return nil, out2, &ResultError{perr}
	}
	return res, out2, nil
}

func readResult(path string) (*ImplementerResult, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("the file was not written")
	}
	if err != nil {
		return nil, err
	}
	return ParseImplementer(data)
}
