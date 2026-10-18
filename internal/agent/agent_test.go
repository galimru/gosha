package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/gitx"
	"github.com/galimru/gosha/internal/tmpl"
)

// fakeClaude writes a shell script standing in for claude. It records its
// args, env and stdin in dir, then runs body.
func fakeClaude(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		`d="` + dir + `"` + "\n" +
		`echo "$@" > "$d/args"; env > "$d/env"; cat > "$d/stdin"` + "\n" +
		`n=$(cat "$d/count" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$d/count"` + "\n" +
		body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func spec(t *testing.T, bin string) Spec {
	t.Helper()
	dir := t.TempDir()
	return Spec{
		Binary: bin, Prompt: "do the thing", Dir: dir, SettingsFile: filepath.Join(dir, "settings.json"),
		Transcript: filepath.Join(dir, "t.jsonl"), OAuthToken: "oauth-secret", ConfigDir: "/cfg", TmpDir: "/tmp/run",
		Git:    Identity{"goshabot[bot]", "1+goshabot[bot]@users.noreply.github.com"},
		Redact: func(s string) string { return strings.ReplaceAll(s, "oauth-secret", "***") },
	}
}

const okStream = `echo '{"type":"system","subtype":"init","session_id":"sess-1"}'
echo '{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1792334400}}'
echo '{"type":"assistant","message":"token oauth-secret"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess-1","permission_denials":[{}]}'`

func TestRun(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-leak")
	t.Setenv("GITHUB_TOKEN", "ghp-must-not-leak")
	bin, dir := fakeClaude(t, okStream)
	s := spec(t, bin)
	s.Model, s.Resume = "opus", "sess-0"
	out, err := Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if out.SessionID != "sess-1" || out.Result != "done" || out.Denials != 1 || out.IsError {
		t.Errorf("outcome = %+v", out)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	want := "-p --output-format stream-json --verbose --settings " + s.SettingsFile +
		" --permission-mode dontAsk --permission-prompts none --model opus --resume sess-0\n"
	if string(args) != want {
		t.Errorf("args = %q\nwant  %q", args, want)
	}
	if in, _ := os.ReadFile(filepath.Join(dir, "stdin")); string(in) != "do the thing" {
		t.Errorf("prompt on stdin = %q", in)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	for _, bad := range []string{"sk-must-not-leak", "ghp-must-not-leak"} {
		if strings.Contains(string(env), bad) {
			t.Errorf("env leaks %s", bad)
		}
	}
	for _, good := range []string{"CLAUDE_CODE_OAUTH_TOKEN=oauth-secret", "CLAUDE_CONFIG_DIR=/cfg", "TMPDIR=/tmp/run", "GIT_COMMITTER_NAME=goshabot[bot]"} {
		if !strings.Contains(string(env), good) {
			t.Errorf("env missing %s", good)
		}
	}
	tr, _ := os.ReadFile(s.Transcript)
	if strings.Contains(string(tr), "oauth-secret") || strings.Count(string(tr), "\n") != 4 {
		t.Errorf("transcript:\n%s", tr)
	}
}

func TestRunModes(t *testing.T) {
	for mode, want := range map[string]string{
		ModeAuto:   "--permission-mode auto --permission-prompts none",
		ModeBypass: "--dangerously-skip-permissions",
	} {
		args := strings.Join(Spec{Mode: mode}.args(), " ")
		if !strings.Contains(args, want) {
			t.Errorf("%s: %s", mode, args)
		}
	}
}

func TestAddDirs(t *testing.T) {
	args := strings.Join(Spec{AddDirs: []string{"/h/runs/X-1/context"}}.args(), " ")
	if !strings.Contains(args, "--add-dir /h/runs/X-1/context") {
		t.Errorf("args = %s", args)
	}
}

func TestRunFailures(t *testing.T) {
	cases := map[string]struct {
		body  string
		check func(error) bool
	}{
		"limit": {
			`echo '{"type":"system","session_id":"s"}'
echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1792334400}}'
echo '{"type":"result","subtype":"error","is_error":true,"result":"limit reached"}'`,
			func(err error) bool {
				var le *LimitError
				return errors.As(err, &le) && le.ResetsAt.Equal(time.Unix(1792334400, 0))
			},
		},
		"warning is not a limit": {
			`echo '{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning"}}'
echo '{"type":"result","subtype":"success","is_error":false}'`,
			func(err error) bool { return err == nil },
		},
		"429": {
			`echo '{"type":"result","subtype":"error","is_error":true,"api_error_status":429}'`,
			func(err error) bool { var le *LimitError; return errors.As(err, &le) },
		},
		"no result": {
			`echo boom >&2; exit 3`,
			func(err error) bool { return err != nil && strings.Contains(err.Error(), "boom") },
		},
		"error result": {
			`echo '{"type":"result","subtype":"error_max_turns","is_error":true,"result":"too long"}'`,
			func(err error) bool { return err != nil && strings.Contains(err.Error(), "error_max_turns") },
		},
	}
	for name, c := range cases {
		bin, _ := fakeClaude(t, c.body)
		if _, err := Run(context.Background(), spec(t, bin)); !c.check(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestRunCancelKillsGroup(t *testing.T) {
	bin, dir := fakeClaude(t, `sleep 300 & echo $! > "$d/child"; echo '{"type":"system","session_id":"s"}'; wait`)
	s := spec(t, bin)
	s.KillGrace = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if _, err := os.Stat(filepath.Join(dir, "child")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	_, err := Run(ctx, s)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	pid, _ := os.ReadFile(filepath.Join(dir, "child"))
	var p int
	json.Unmarshal([]byte(strings.TrimSpace(string(pid))), &p)
	if p == 0 {
		t.Fatalf("no child pid: %q", pid)
	}
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(p, 0); err == nil {
		t.Errorf("child %d still running", p)
	}
}

func TestRunBackgroundChildDoesntHang(t *testing.T) {
	// A background process holding stdout must not keep Run waiting.
	bin, _ := fakeClaude(t, `sleep 300 &
echo '{"type":"result","subtype":"success","is_error":false,"session_id":"s"}'`)
	done := make(chan error, 1)
	go func() { _, err := Run(context.Background(), spec(t, bin)); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run hung on a background child")
	}
}

func TestImplementRetry(t *testing.T) {
	res := filepath.Join(t.TempDir(), "result.json")
	bin, dir := fakeClaude(t, `if [ "$n" = 1 ]; then echo '{"status":"done"}' > `+res+`; else
echo '{"status":"done","summary":"added login","verification":"go test"}' > `+res+`; fi
echo '{"type":"result","subtype":"success","is_error":false,"session_id":"sess-9"}'`)
	r, out, err := Implement(context.Background(), spec(t, bin), res)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary != "added login" || out.SessionID != "sess-9" {
		t.Errorf("result = %+v, %+v", r, out)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if !strings.Contains(string(args), "--resume sess-9") || !strings.Contains(string(stdin), `"summary" is required`) {
		t.Errorf("retry: args %q, prompt %q", args, stdin)
	}

	// Still invalid after the retry: a ResultError.
	bin, _ = fakeClaude(t, `echo '{"type":"result","subtype":"success","is_error":false,"session_id":"s"}'`)
	var rerr *ResultError
	if _, _, err := Implement(context.Background(), spec(t, bin), res); !errors.As(err, &rerr) {
		t.Errorf("err = %v", err)
	}
}

func TestParseImplementer(t *testing.T) {
	good := []string{
		`{"status":"done","summary":"x"}`,
		`{"status":"blocked","blocked_reason":"unclear","questions":["which?"]}`,
	}
	for _, g := range good {
		if _, err := ParseImplementer([]byte(g)); err != nil {
			t.Errorf("%s: %v", g, err)
		}
	}
	bad := []string{`{"status":"done"}`, `{"status":"blocked"}`, `{"status":"maybe","summary":"x"}`, `{"status":"done","summary":"x","extra":1}`, `nope`}
	for _, b := range bad {
		if _, err := ParseImplementer([]byte(b)); err == nil {
			t.Errorf("%s: expected an error", b)
		}
	}
}

func TestSettings(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"projects/demo/repo", "projects/demo/worktrees/NXT-1", "projects/demo/worktrees/NXT-2",
		"projects/demo/runs/NXT-1/3", "projects/other", "logs"} {
		os.MkdirAll(filepath.Join(home, d), 0o700)
	}
	for _, f := range []string{"secrets.env", "gosha.db", "projects/demo/secrets.env", "projects/demo/runs/NXT-1/3-implement.jsonl"} {
		os.WriteFile(filepath.Join(home, f), nil, 0o600)
	}
	p := Paths{
		Home: home, Checkout: filepath.Join(home, "projects/demo/worktrees/NXT-1"),
		RunDir: filepath.Join(home, "projects/demo/runs/NXT-1/3"), UserHome: "/u", Writable: []string{"~/.cargo", "/opt/x"},
	}
	data, err := Settings(p, RoleImplementer, true)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Permissions struct{ Allow, Deny []string }
		Sandbox     struct {
			FailIfUnavailable        bool
			AllowUnsandboxedCommands bool
			Filesystem               struct{ DenyRead, AllowRead, AllowWrite []string }
		}
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	denied := func(rel string) bool {
		return slices.Contains(s.Permissions.Deny, "Read(/"+filepath.Join(home, rel)+")")
	}
	for _, rel := range []string{"secrets.env", "gosha.db", "logs", "projects/other", "projects/demo/repo", "projects/demo/secrets.env",
		"projects/demo/worktrees/NXT-2", "projects/demo/runs/NXT-1/3-implement.jsonl"} {
		if !denied(rel) {
			t.Errorf("%s not denied", rel)
		}
	}
	for _, rel := range []string{"projects", "projects/demo", "projects/demo/worktrees/NXT-1", "projects/demo/runs/NXT-1/3"} {
		if denied(rel) {
			t.Errorf("%s denied", rel)
		}
	}
	if !slices.Contains(s.Permissions.Allow, "Edit(/"+p.Checkout+"/**)") {
		t.Errorf("checkout not editable: %v", s.Permissions.Allow)
	}
	fs := s.Sandbox.Filesystem
	if !s.Sandbox.FailIfUnavailable || s.Sandbox.AllowUnsandboxedCommands || !slices.Equal(fs.DenyRead, []string{home}) ||
		!slices.Contains(fs.AllowRead, p.Checkout) || !slices.Contains(fs.AllowWrite, "/u/.cargo") ||
		!slices.Contains(fs.AllowWrite, "/opt/x") || !slices.Contains(fs.AllowWrite, "/u/.cache") {
		t.Errorf("sandbox = %+v", s.Sandbox)
	}

	data, _ = Settings(p, RoleImplementer, false)
	if strings.Contains(string(data), `"sandbox"`) {
		t.Errorf("sandbox block with sandbox off:\n%s", data)
	}
}

func TestDefaultPrompt(t *testing.T) {
	// No repo override: the embedded default is used.
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", "--initial-branch=main"}, {"commit", "--quiet", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(gitx.Env(""), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	d := tmpl.Data{
		Project: "demo", Branch: "gosha/NXT-12-add-login", BaseBranch: "main", ResultFile: "/r/result.json",
		Ticket: tmpl.Ticket{ID: "NXT-12", Title: "Add login", Description: "Users log in.",
			Comments: []tmpl.Comment{{Author: "Ann", Body: "Use OAuth."}}},
	}
	got, err := RenderPrompt(context.Background(), gitx.Open(filepath.Join(dir, ".git")), "HEAD", "implement.md", d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# NXT-12: Add login", "Users log in.", "**Ann:**", "Use OAuth.", "`gosha/NXT-12-add-login`", "`/r/result.json`"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Project context") {
		t.Error("context section without a context dir")
	}
}
