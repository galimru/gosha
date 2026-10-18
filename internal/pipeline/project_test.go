package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/gitx"
	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/store"
)

type recorder struct {
	mu     sync.Mutex
	events []notify.Event
}

func (r *recorder) Send(ev notify.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) Flush(context.Context) error { return nil }

func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, ev := range r.events {
		out = append(out, ev.Text)
	}
	r.events = nil
	return out
}

// upstream is a work repo whose commits the project's bare clone fetches.
type upstream struct {
	t   *testing.T
	dir string
}

func (u upstream) commit(config string) {
	u.t.Helper()
	path := filepath.Join(u.dir, ".gosha", "config.yaml")
	if config == "" {
		os.Remove(path)
	} else {
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(config), 0o644)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "--quiet", "--allow-empty", "-m", "change"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = u.dir
		cmd.Env = append(gitx.Env(""), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			u.t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestReadConfigFailClosed(t *testing.T) {
	ctx := context.Background()
	u := upstream{t, t.TempDir()}
	if out, err := exec.Command("git", "-C", u.dir, "init", "--quiet", "--initial-branch=main").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	valid := "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\n"
	u.commit(valid)

	home := config.Home(t.TempDir())
	os.WriteFile(home.Secrets(), []byte("LINEAR_CLIENT_ID=id\nLINEAR_CLIENT_SECRET=client-secret-value\nNOTION_TOKEN=notion-token\nCLAUDE_CODE_OAUTH_TOKEN=claude-token\n"), 0o600)
	repo, err := gitx.CloneBare(ctx, "file://"+u.dir, home.Project("demo").Repo(), "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "gosha.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec := &recorder{}
	env := &Env{Home: home, Store: st, Notify: rec, Log: slog.New(slog.DiscardHandler), Redactor: config.NewRedactor()}

	step := func(cfg string) (*Project, error) {
		t.Helper()
		u.commit(cfg)
		if err := repo.Fetch(ctx, ""); err != nil {
			t.Fatal(err)
		}
		p := &Project{Name: "demo", Repo: repo}
		err := env.readConfig(ctx, p)
		if err == nil {
			env.resume(ctx, p)
		}
		return p, err
	}
	state := func() *store.ProjectState {
		ps, _ := st.ProjectState(ctx, "demo")
		return ps
	}

	p, err := step(valid)
	if err != nil || p.Config.Linear.Team != "NXT" || p.Base != "main" {
		t.Fatalf("valid config: %+v, %v", p, err)
	}
	if ev := rec.take(); len(ev) != 0 {
		t.Errorf("healthy first load shouldn't notify: %v", ev)
	}

	// Invalid: paused, with one notification naming the commit.
	p, err = step("name: demo\nlinear: {}\n")
	var perr *PausedError
	if !errors.As(err, &perr) || p.Config != nil {
		t.Fatalf("expected PausedError, got %v", err)
	}
	if s := state(); s.Status != store.StatusPaused || s.Commit != p.Commit {
		t.Errorf("state = %+v", s)
	}
	if ev := rec.take(); len(ev) != 1 || !strings.Contains(ev[0], p.Commit[:7]) || !strings.Contains(ev[0], "paused") {
		t.Errorf("pause events = %v", ev)
	}

	// Re-checking the same commit (every poll) doesn't notify again.
	if err := env.readConfig(ctx, &Project{Name: "demo", Repo: repo}); err == nil {
		t.Fatal("expected an error")
	}
	if ev := rec.take(); len(ev) != 0 {
		t.Errorf("repeated pause notified again: %v", ev)
	}

	// Removed from the default branch: paused too.
	if _, err := step(""); !errors.As(err, &perr) || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing config: %v", err)
	}
	rec.take()

	// Wrong name: paused, never run under another project's name.
	if _, err := step(strings.Replace(valid, "demo", "other", 1)); !errors.As(err, &perr) {
		t.Fatalf("name mismatch: %v", err)
	}
	rec.take()

	// Valid again: resumed, one notification.
	if _, err := step(valid); err != nil {
		t.Fatal(err)
	}
	if s := state(); s.Status != store.StatusActive {
		t.Errorf("state = %+v", s)
	}
	if ev := rec.take(); len(ev) != 1 || !strings.Contains(ev[0], "resumed") {
		t.Errorf("resume events = %v", ev)
	}
}

func TestPauseReasonRedacted(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "gosha.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	red := config.NewRedactor()
	red.Add("hunter2-secret")
	rec := &recorder{}
	env := &Env{Store: st, Notify: rec, Log: slog.New(slog.DiscardHandler), Redactor: red}
	env.pause(ctx, &Project{Name: "demo", Commit: "abcdef1"}, errors.New("bad value hunter2-secret"))
	ps, _ := st.ProjectState(ctx, "demo")
	if strings.Contains(ps.Reason, "hunter2") || strings.Contains(strings.Join(rec.take(), ""), "hunter2") {
		t.Errorf("secret leaked: %q", ps.Reason)
	}
}
