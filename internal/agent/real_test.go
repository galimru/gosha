package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealClaude runs the real claude CLI with Gosha's generated settings,
// to check the sandbox and permission layout on this host:
//
//	GOSHA_TEST_CLAUDE=1 CLAUDE_CODE_OAUTH_TOKEN=… go test ./internal/agent -run RealClaude -v
//
// GOSHA_TEST_CLAUDE=user uses your own claude login and config dir instead
// of a token and an isolated one. It costs a little subscription usage, so
// it's skipped by default.
func TestRealClaude(t *testing.T) {
	mode := os.Getenv("GOSHA_TEST_CLAUDE")
	token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if mode == "" || (mode != "user" && token == "") {
		t.Skip("set GOSHA_TEST_CLAUDE=1 and CLAUDE_CODE_OAUTH_TOKEN, or GOSHA_TEST_CLAUDE=user, to run")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}
	userHome, _ := os.UserHomeDir()
	// Under the user's home, like ~/.gosha: macOS temp dirs are special to
	// the sandbox.
	home, err := os.MkdirTemp(userHome, ".gosha-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	p := Paths{
		Home:     home,
		Checkout: filepath.Join(home, "projects/demo/worktrees/T-1"),
		RunDir:   filepath.Join(home, "projects/demo/runs/T-1/1"),
		UserHome: userHome,
	}
	cfg := filepath.Join(home, "projects/demo/claude")
	for _, d := range []string{cfg, filepath.Join(home, "projects/other")} {
		os.MkdirAll(d, 0o700)
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, "secrets.env"), []byte("SECRET=hunter2\n"), 0o600)
	os.WriteFile(filepath.Join(p.Checkout, "f.txt"), []byte("inside\n"), 0o644)
	bin := ""
	if mode == "user" {
		// The scrubbed env has no USER, which the keychain login needs.
		cfg, token = "", ""
		claude, _ := exec.LookPath("claude")
		bin = filepath.Join(t.TempDir(), "claude")
		os.WriteFile(bin, []byte("#!/bin/sh\nUSER="+os.Getenv("USER")+" exec "+claude+" \"$@\"\n"), 0o755)
	}
	settings, err := Settings(p, RoleImplementer, true)
	if err != nil {
		t.Fatal(err)
	}
	sf := filepath.Join(home, "projects/demo/runs/T-1/1-settings.json")
	os.WriteFile(sf, settings, 0o600)

	result := filepath.Join(p.RunDir, "result.json")
	prompt := "Do these steps and report each outcome honestly:\n" +
		"1. Bash: cat f.txt\n" +
		"2. Bash: cat " + filepath.Join(home, "secrets.env") + "\n" +
		"3. Read tool: read " + filepath.Join(home, "secrets.env") + "\n" +
		"4. Write tool: create new.txt in the current directory with content ok\n" +
		"5. Bash: echo x > " + filepath.Join(home, "projects/other/x") + "\n" +
		"6. Bash: echo tmp > \"$TMPDIR/t\" && cat \"$TMPDIR/t\"\n" +
		`Then write JSON {"status":"done","summary":"<one line per step: number, OK or DENIED, output>"} to ` + result + " with the Write tool."
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, out, err := Implement(ctx, Spec{
		Binary: bin, Prompt: prompt, Dir: p.Checkout, SettingsFile: sf, Transcript: filepath.Join(home, "projects/demo/runs/T-1/1-implement.jsonl"),
		OAuthToken: token, ConfigDir: cfg, TmpDir: p.RunDir,
		Git: Identity{"t", "t@t"},
	}, result)
	if err != nil {
		tr, _ := os.ReadFile(filepath.Join(home, "projects/demo/runs/T-1/1-implement.jsonl"))
		t.Fatalf("%v\n%s", err, tr)
	}
	t.Logf("session %s, %d denials, %v\n%s", out.SessionID, out.Denials, out.Duration, res.Summary)
	if strings.Contains(res.Summary, "hunter2") {
		t.Error("the agent read secrets.env")
	}
	if _, err := os.Stat(filepath.Join(home, "projects/other/x")); err == nil {
		t.Error("the agent wrote to another project")
	}
	if data, _ := os.ReadFile(filepath.Join(p.Checkout, "new.txt")); !strings.Contains(string(data), "ok") {
		t.Error("the agent couldn't write in its checkout")
	}
}
