package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Permission modes (project agent.permission_mode, DESIGN §7.1).
const (
	ModeDontAsk = "dontask"
	ModeAuto    = "auto"
	ModeBypass  = "bypass"
)

// Identity is the git author and committer for the agent's commits.
type Identity struct{ Name, Email string }

// Spec is one claude invocation.
type Spec struct {
	// Binary is the claude executable; "claude" (from PATH) if empty.
	Binary string
	Prompt string
	// Dir is the working directory (the ticket checkout).
	Dir string
	// SettingsFile is the generated settings (outside the agent's reach).
	SettingsFile string
	// Transcript receives the redacted stream-json; it's appended to, so a
	// retry lands in the same file.
	Transcript string
	Mode       string
	Model      string
	// Resume continues an earlier session.
	Resume string
	// DisallowedTools are removed from the agent, whatever the permission
	// mode: the reviewer can't edit (DESIGN §7.1).
	DisallowedTools []string
	// AddDirs are extra directories the agent may use beyond Dir: the
	// run's frozen project context (read-only through the settings).
	AddDirs []string
	// Env.
	OAuthToken string
	ConfigDir  string // CLAUDE_CONFIG_DIR
	TmpDir     string // TMPDIR
	Git        Identity
	// Redact scrubs secrets from the transcript; nil keeps lines as they are.
	Redact func(string) string
	// KillGrace is how long the process group gets after SIGTERM (5s default).
	KillGrace time.Duration
}

func (s Spec) args() []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--settings", s.SettingsFile}
	switch s.Mode {
	case ModeAuto:
		args = append(args, "--permission-mode", "auto", "--permission-prompts", "none")
	case ModeBypass:
		args = append(args, "--dangerously-skip-permissions")
	default:
		args = append(args, "--permission-mode", "dontAsk", "--permission-prompts", "none")
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.Resume != "" {
		args = append(args, "--resume", s.Resume)
	}
	if len(s.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(s.DisallowedTools, ","))
	}
	for _, d := range s.AddDirs {
		args = append(args, "--add-dir", d)
	}
	return args
}

// env is the agent's whole environment: no Gosha secrets, no
// ANTHROPIC_API_KEY, no GitHub or Linear tokens (DESIGN §7.1).
func (s Spec) env() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + s.TmpDir,
		"GIT_AUTHOR_NAME=" + s.Git.Name,
		"GIT_AUTHOR_EMAIL=" + s.Git.Email,
		"GIT_COMMITTER_NAME=" + s.Git.Name,
		"GIT_COMMITTER_EMAIL=" + s.Git.Email,
	}
	if s.ConfigDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+s.ConfigDir)
	}
	if s.OAuthToken != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+s.OAuthToken)
	}
	return env
}

// Outcome is what a finished run reported.
type Outcome struct {
	SessionID string
	IsError   bool
	// Result is the final message text.
	Result   string
	Denials  int
	Duration time.Duration
}

// LimitError means Claude's usage limit stopped the run (DESIGN §8.11).
type LimitError struct {
	Status   string
	ResetsAt time.Time
}

func (e *LimitError) Error() string {
	if e.ResetsAt.IsZero() {
		return "claude usage limit (" + e.Status + ")"
	}
	return fmt.Sprintf("claude usage limit (%s), resets %s", e.Status, e.ResetsAt.Local().Format("15:04"))
}

// event is the part of a stream-json line Gosha reads.
type event struct {
	Type          string `json:"type"`
	Subtype       string `json:"subtype"`
	SessionID     string `json:"session_id"`
	IsError       bool   `json:"is_error"`
	Result        string `json:"result"`
	APIError      int    `json:"api_error_status"`
	Denials       []any  `json:"permission_denials"`
	RateLimitInfo *struct {
		Status   string `json:"status"`
		ResetsAt int64  `json:"resetsAt"`
	} `json:"rate_limit_info"`
}

// limited reports a rate-limit status that blocks requests. Only "allowed"
// was observed in M0; the warning variant still lets requests through.
func limited(status string) bool {
	return status != "" && status != "allowed" && status != "allowed_warning"
}

// Run starts claude in its own process group, feeds the prompt on stdin,
// saves the stream and parses it. Cancelling ctx kills the whole group.
// A usage limit that stopped the run is a *LimitError.
func Run(ctx context.Context, s Spec) (*Outcome, error) {
	bin := s.Binary
	if bin == "" {
		bin = "claude"
	}
	tr, err := os.OpenFile(s.Transcript, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer tr.Close()

	cmd := exec.Command(bin, s.args()...)
	cmd.Dir = s.Dir
	cmd.Env = s.env()
	cmd.Stdin = strings.NewReader(s.Prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Plain pipes, so Wait returns when claude exits even if something it
	// started still holds them open; killing the group then closes them.
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer outR.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		outW.Close()
		return nil, err
	}
	defer errR.Close()
	cmd.Stdout, cmd.Stderr = outW, errW
	start := time.Now()
	err = cmd.Start()
	outW.Close()
	errW.Close()
	if err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}
	pgid := cmd.Process.Pid

	stop := make(chan struct{})
	var killed sync.WaitGroup
	killed.Add(1)
	go func() {
		defer killed.Done()
		select {
		case <-stop:
		case <-ctx.Done():
			syscall.Kill(-pgid, syscall.SIGTERM)
			grace := s.KillGrace
			if grace == 0 {
				grace = 5 * time.Second
			}
			select {
			case <-stop:
			case <-time.After(grace):
				syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
	}()

	var stderr tailBuffer
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		io.Copy(&stderr, errR)
	}()
	out := &Outcome{}
	var limit *LimitError
	var result *event
	go func() {
		defer readers.Done()
		rd := bufio.NewReader(outR)
		for {
			line, rerr := rd.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				text := string(line)
				if s.Redact != nil {
					text = s.Redact(text)
				}
				if !strings.HasSuffix(text, "\n") {
					text += "\n"
				}
				tr.WriteString(text)
				var ev event
				if json.Unmarshal(line, &ev) == nil {
					switch ev.Type {
					case "system":
						if ev.SessionID != "" {
							out.SessionID = ev.SessionID
						}
					case "rate_limit_event":
						if i := ev.RateLimitInfo; i != nil && limited(i.Status) {
							limit = &LimitError{Status: i.Status}
							if i.ResetsAt > 0 {
								limit.ResetsAt = time.Unix(i.ResetsAt, 0)
							}
						}
					case "result":
						result = &ev
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	werr := cmd.Wait()
	close(stop)
	killed.Wait()
	// Anything the agent left running in the background goes too.
	syscall.Kill(-pgid, syscall.SIGKILL)
	readers.Wait()
	out.Duration = time.Since(start)

	if ctx.Err() != nil {
		return out, fmt.Errorf("claude stopped: %w", context.Cause(ctx))
	}
	if result != nil {
		if result.SessionID != "" {
			out.SessionID = result.SessionID
		}
		out.IsError, out.Result, out.Denials = result.IsError, result.Result, len(result.Denials)
	}
	if limit != nil && (result == nil || result.IsError) {
		return out, limit
	}
	if result != nil && result.IsError && result.APIError == 429 {
		return out, &LimitError{Status: "429"}
	}
	if result == nil {
		msg := strings.TrimSpace(stderr.String())
		if werr != nil && msg == "" {
			msg = werr.Error()
		}
		return out, fmt.Errorf("claude exited without a result: %s", msg)
	}
	if result.IsError {
		return out, fmt.Errorf("claude run failed (%s): %s", result.Subtype, strings.TrimSpace(result.Result))
	}
	return out, nil
}

// tailBuffer keeps the last 4 KiB written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - 4096; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
