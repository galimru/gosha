package config

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var sharedVars = map[string]string{
	"LINEAR_CLIENT_ID":     "lin-client-id",
	"LINEAR_CLIENT_SECRET": "lin-client-secret",
	"NOTION_TOKEN":         "notion-secret-token",
}

func testSecrets(layers ...map[string]string) *Secrets {
	return NewSecrets(nil, NewRedactor(), layers...)
}

const minimalProject = `
name: nextdesk
linear:
  team: NXT
notion:
  root_page: https://www.notion.so/NextDesk-1a2b3c4d5e6f708192a3b4c5d6e7f809
`

func TestProjectDefaults(t *testing.T) {
	p, warns, err := ParseProject([]byte(minimalProject), "a1b2c3d4e5", testSecrets(sharedVars))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if p.Linear.ClientSecret != "lin-client-secret" || p.Notion.Token != "notion-secret-token" {
		t.Errorf("secret defaults not applied: %+v", p.Linear)
	}
	if p.Linear.States != (States{"Ready", "In Progress", "In Review", "Blocked"}) {
		t.Errorf("state defaults: %+v", p.Linear.States)
	}
	if p.Checks.StartGrace != 3*time.Minute || p.Agent.Timeout != time.Hour || p.Review.MaxRounds != 3 {
		t.Errorf("limit defaults: %+v %+v", p.Checks, p.Agent)
	}
	if p.Agent.PermissionMode != "dontask" || p.GitHub.MergeMethod != "squash" {
		t.Errorf("mode defaults: %+v %+v", p.Agent, p.GitHub)
	}
	if b, _ := p.BranchName(SampleTicket); b != "gosha/NXT-12-add-login" {
		t.Errorf("branch = %q", b)
	}
}

func TestProjectStateOverrideSubset(t *testing.T) {
	src := strings.Replace(minimalProject, "team: NXT", "team: NXT\n  states:\n    ready: Queued", 1)
	p, _, err := ParseProject([]byte(src), "", testSecrets(sharedVars))
	if err != nil {
		t.Fatal(err)
	}
	if p.Linear.States.Ready != "Queued" || p.Linear.States.InProgress != "In Progress" {
		t.Errorf("states = %+v", p.Linear.States)
	}
}

func TestProjectErrors(t *testing.T) {
	cases := map[string]struct {
		src, field, msg string
		vars            map[string]string
	}{
		"literal secret": {
			src:   minimalProject + "  token: abc123\n",
			field: "notion.token", msg: "never a literal",
		},
		"literal secret inside a reference": {
			src:   strings.Replace(minimalProject, "team: NXT", "team: NXT\n  client_secret: x${LINEAR_CLIENT_SECRET}", 1),
			field: "linear.client_secret", msg: "never a literal",
		},
		"missing variable": {
			src:   minimalProject,
			field: "notion.token", msg: "${NOTION_TOKEN}",
			vars: map[string]string{"LINEAR_CLIENT_ID": "x", "LINEAR_CLIENT_SECRET": "y"},
		},
		"empty variable": {
			src:   minimalProject,
			field: "notion.token", msg: "not set",
			vars: map[string]string{"LINEAR_CLIENT_ID": "x", "LINEAR_CLIENT_SECRET": "y", "NOTION_TOKEN": ""},
		},
		"unknown field": {
			src: minimalProject + "extra: 1\n", msg: "field extra not found",
		},
		"missing team": {
			src:   strings.Replace(minimalProject, "team: NXT", "team: \"\"", 1),
			field: "linear.team", msg: "required",
		},
		"bad branch": {
			src:   minimalProject + "github:\n  branch: \"gosha/{{.Ticket.Title}}\"\n",
			field: "github.branch", msg: "cannot contain ' '",
		},
		"bad branch field": {
			src:   minimalProject + "github:\n  branch: \"{{.Ticket.Nope}}\"\n",
			field: "github.branch", msg: "Nope",
		},
		"bad permission mode": {
			src:   minimalProject + "agent:\n  permission_mode: yolo\n",
			field: "agent.permission_mode",
		},
		"bad yaml": {src: "name: [", msg: "yaml"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			vars := c.vars
			if vars == nil {
				vars = sharedVars
			}
			_, _, err := ParseProject([]byte(c.src), "abcdef1234", testSecrets(vars))
			if err == nil {
				t.Fatal("expected an error")
			}
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("not a FieldError: %v", err)
			}
			if fe.File != ".gosha/config.yaml@abcdef1" {
				t.Errorf("file = %q", fe.File)
			}
			if c.field != "" && fe.Field != c.field {
				t.Errorf("field = %q, want %q (%v)", fe.Field, c.field, err)
			}
			if !strings.Contains(err.Error(), c.msg) {
				t.Errorf("error %q doesn't mention %q", err, c.msg)
			}
		})
	}
}

func TestProjectWarnings(t *testing.T) {
	src := minimalProject + "github:\n  branch: \"bot/{{.Ticket.Title | slug}}\"\nagent:\n  permission_mode: bypass\n"
	_, warns, err := ParseProject([]byte(src), "", testSecrets(sharedVars))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 2 {
		t.Errorf("warnings = %v", warns)
	}
}

func TestExpand(t *testing.T) {
	s := testSecrets(map[string]string{"A": "alpha", "B": "b#:x"})
	cases := map[string]string{
		"plain":         "plain",
		"$HOME and $1":  "$HOME and $1",
		"${A}-${B}":     "alpha-b#:x",
		"$${A}":         "${A}",
		"price $5 ${A}": "price $5 alpha",
		"^v[0-9]+$":     "^v[0-9]+$",
	}
	for in, want := range cases {
		got, err := Expand(in, s)
		if err != nil || got != want {
			t.Errorf("Expand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"${MISSING}", "${A", "${1X}"} {
		if _, err := Expand(in, s); err == nil {
			t.Errorf("Expand(%q): expected an error", in)
		}
	}
}

func TestExpandKeepsStructure(t *testing.T) {
	// A value with YAML syntax in it must stay one string.
	s := testSecrets(map[string]string{"V": "a: b # c\n- d"})
	var out struct {
		Field string `yaml:"field"`
	}
	if err := decode("f", []byte("field: ${V}\n"), s, &out); err != nil {
		t.Fatal(err)
	}
	if out.Field != "a: b # c\n- d" {
		t.Errorf("field = %q", out.Field)
	}
}

func TestSecretsPrecedence(t *testing.T) {
	env := func(k string) (string, bool) {
		v, ok := map[string]string{"K": "from-env", "ONLY_ENV": "env-only"}[k]
		return v, ok
	}
	s := NewSecrets(env, NewRedactor(), map[string]string{"K": "from-project"}, map[string]string{"K": "from-shared", "S": "shared-only"})
	for k, want := range map[string]string{"K": "from-project", "S": "shared-only", "ONLY_ENV": "env-only"} {
		if v, _ := s.Lookup(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
}

func TestReadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.env")
	content := "# comment\n\nA=1\nexport B = \"two words\"\nC='x=y'\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := ReadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["B"] != "two words" || m["C"] != "x=y" {
		t.Errorf("parsed %v", m)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEnvFile(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("expected a permissions error, got %v", err)
	}

	if m, err := ReadEnvFile(filepath.Join(dir, "missing.env")); err != nil || len(m) != 0 {
		t.Errorf("missing file: %v %v", m, err)
	}

	bad := filepath.Join(dir, "bad.env")
	os.WriteFile(bad, []byte("not a pair\n"), 0o600)
	if _, err := ReadEnvFile(bad); err == nil {
		t.Error("expected a parse error")
	}
}

func TestGlobal(t *testing.T) {
	h := Home(t.TempDir())
	if err := os.WriteFile(h.Path("github-app.pem"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := testSecrets(map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat", "TELEGRAM_BOT_TOKEN": "123:abc"})
	g, err := ParseGlobal(h, "config.yaml", []byte("github_app:\n  app_id: 42\ntelegram:\n  chat_id: -100\n"), s)
	if err != nil {
		t.Fatal(err)
	}
	if g.Concurrency != 1 || !g.Agent.SandboxEnabled() || g.Telegram.Digest() != "09:00" || g.Telegram.BotToken != "123:abc" {
		t.Errorf("defaults: %+v", g)
	}
	if g.GitHubApp.PrivateKeyFile != h.Path("github-app.pem") {
		t.Errorf("key path = %q", g.GitHubApp.PrivateKeyFile)
	}

	g, err = ParseGlobal(h, "config.yaml", []byte("github_app:\n  app_id: 42\ntelegram:\n  chat_id: -100\n  digest_at: \"\"\nagent:\n  sandbox: false\n"), s)
	if err != nil {
		t.Fatal(err)
	}
	if g.Telegram.Digest() != "" || g.Agent.SandboxEnabled() {
		t.Errorf("explicit values ignored: %+v", g)
	}

	os.Chmod(h.Path("github-app.pem"), 0o644)
	if _, err := ParseGlobal(h, "config.yaml", []byte("github_app:\n  app_id: 42\ntelegram:\n  chat_id: -100\n"), s); err == nil {
		t.Error("expected a key permissions error")
	}
}

func TestRedactor(t *testing.T) {
	r := NewRedactor()
	r.Add("short")
	r.Add("supersecret")
	r.Add("supersecret-long")
	if got := r.Redact("a supersecret-long and supersecret, short"); got != "a *** and ***, short" {
		t.Errorf("Redact = %q", got)
	}

	var buf bytes.Buffer
	log := slog.New(r.Handler(slog.NewTextHandler(&buf, nil)))
	log.With("tok", "supersecret").Info("got supersecret", "err", errors.New("bad supersecret"), slog.Group("g", "v", "supersecret"))
	if strings.Contains(buf.String(), "supersecret") {
		t.Errorf("secret leaked into log: %s", buf.String())
	}
}

func TestProjects(t *testing.T) {
	h := Home(t.TempDir())
	for _, d := range []string{"b/repo", "a/repo", ".tmp-x/repo", "norepo"} {
		os.MkdirAll(filepath.Join(h.ProjectsDir(), d), 0o700)
	}
	names, err := h.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "a,b" {
		t.Errorf("projects = %v", names)
	}
}
