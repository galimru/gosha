package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/galimru/gosha/internal/agent"
	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/gitx"
	"github.com/galimru/gosha/internal/linear"
	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/notion"
	"github.com/galimru/gosha/internal/store"
)

// Env is what every project operation needs.
type Env struct {
	Home     config.Home
	Global   *config.Global
	App      *github.App
	Store    *store.Store
	Notify   notify.Notifier
	Log      *slog.Logger
	Redactor *config.Redactor
	// ClaudeBinary is the claude executable; "claude" from PATH if empty.
	ClaudeBinary string
	// Implement runs the implementer: agent.Implement unless replaced in tests.
	Implement func(context.Context, agent.Spec, string) (*agent.ImplementerResult, *agent.Outcome, error)
	// Review runs the reviewer: agent.Review unless replaced in tests.
	Review func(context.Context, agent.Spec, string) (*agent.ReviewerResult, *agent.Outcome, error)
	// Notion makes the Notion client for a token: notion.New unless
	// replaced in tests.
	Notion func(token string) notion.API

	botMu sync.Mutex
	bot   *agent.Identity

	slotsOnce sync.Once
	slots     chan struct{}
}

// acquireSlot waits for one of the concurrency agent slots shared by every
// project in the process.
func (e *Env) acquireSlot(ctx context.Context) (release func(), err error) {
	e.slotsOnce.Do(func() {
		n := 1
		if e.Global != nil && e.Global.Concurrency > 0 {
			n = e.Global.Concurrency
		}
		e.slots = make(chan struct{}, n)
	})
	select {
	case e.slots <- struct{}{}:
		return func() { <-e.slots }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (e *Env) implement(ctx context.Context, s agent.Spec, resultFile string) (*agent.ImplementerResult, *agent.Outcome, error) {
	if e.Implement != nil {
		return e.Implement(ctx, s, resultFile)
	}
	return agent.Implement(ctx, s, resultFile)
}

func (e *Env) review(ctx context.Context, s agent.Spec, resultFile string) (*agent.ReviewerResult, *agent.Outcome, error) {
	if e.Review != nil {
		return e.Review(ctx, s, resultFile)
	}
	return agent.Review(ctx, s, resultFile)
}

// realHome is the home with symlinks resolved: the sandbox matches real
// paths.
func (e *Env) realHome() (config.Home, error) {
	home, err := filepath.EvalSymlinks(e.Home.Dir())
	return config.Home(home), err
}

// Project is a project with its config freshly loaded from the default branch.
type Project struct {
	Name    string
	Config  *config.Project
	Creds   config.Credentials
	Repo    *gitx.Repo
	GitHub  gitx.GitHubRepo
	Install *github.Installation
	Base    string // default branch
	Commit  string // origin/<base> the config was read from
}

// PausedError means the project is paused because of its config: no
// pickups until a valid config lands on the default branch (DESIGN §5.2).
type PausedError struct {
	Project string
	Commit  string
	Err     error
}

func (e *PausedError) Error() string {
	return fmt.Sprintf("%s: paused: %v", e.Project, e.Err)
}

func (e *PausedError) Unwrap() error { return e.Err }

// LoadProject fetches the project's bare clone and reads .gosha/config.yaml
// from origin/<default>. Config problems pause the project (fail closed);
// GitHub being unreachable doesn't, it's just an error to retry later.
func (e *Env) LoadProject(ctx context.Context, name string) (*Project, error) {
	p := &Project{Name: name, Repo: gitx.Open(e.Home.Project(name).Repo())}
	origin, err := p.Repo.OriginURL(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if p.GitHub, err = gitx.ParseGitHubURL(origin); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if p.Install, err = e.App.Installation(ctx, p.GitHub); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	tok, err := p.Install.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := p.Repo.Fetch(ctx, tok); err != nil {
		return nil, fmt.Errorf("%s: fetch: %w", name, err)
	}
	if err := e.readConfig(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// readConfig reads the config from the freshly fetched origin/<default>,
// pausing the project if it's missing or invalid. Resuming is left to the
// caller, once every check that can pause the project has passed.
func (e *Env) readConfig(ctx context.Context, p *Project) error {
	var err error
	if p.Base, err = p.Repo.DefaultBranch(ctx); err != nil {
		return fmt.Errorf("%s: %w", p.Name, err)
	}
	if p.Commit, err = p.Repo.RevParse(ctx, "origin/"+p.Base); err != nil {
		return fmt.Errorf("%s: %w", p.Name, err)
	}
	cfg, err := e.parseConfig(ctx, p)
	if err != nil {
		return e.pause(ctx, p, err)
	}
	secrets, err := config.LoadSecrets(e.Home, p.Name, e.Redactor)
	if err != nil {
		return e.pause(ctx, p, err)
	}
	if p.Creds, err = secrets.ProjectCredentials(); err != nil {
		return e.pause(ctx, p, err)
	}
	p.Config = cfg
	if cfg.GitHub.DefaultBranch != "" {
		p.Base = cfg.GitHub.DefaultBranch
	}
	return nil
}

func (e *Env) parseConfig(ctx context.Context, p *Project) (*config.Project, error) {
	data, err := p.Repo.Show(ctx, p.Commit, config.ProjectFile)
	if errors.Is(err, gitx.ErrNotFound) {
		return nil, fmt.Errorf("%s is missing on %s", config.ProjectFile, p.Base)
	}
	if err != nil {
		return nil, err
	}
	cfg, err := config.ParseProject(data, p.Commit)
	if err != nil {
		return nil, err
	}
	if cfg.Name != p.Name {
		return nil, fmt.Errorf("%s says name %q but the project is registered as %q", config.ProjectFile, cfg.Name, p.Name)
	}
	return cfg, nil
}

// Linear is a project's Linear access with its team and states resolved.
type Linear struct {
	Client *linear.Client
	Team   *linear.Team
	States *linear.States
}

// ConnectLinear checks the project's team and states. A missing team or
// state pauses the project, since Gosha can't run it (DESIGN §3).
func (e *Env) ConnectLinear(ctx context.Context, p *Project) (*Linear, error) {
	c := linear.New(p.Creds.LinearClientID, p.Creds.LinearClientSecret)
	team, err := c.Team(ctx, p.Config.Linear.Team)
	if errors.Is(err, linear.ErrTeamNotFound) {
		return nil, e.pause(ctx, p, err)
	}
	if err != nil {
		return nil, err
	}
	st, err := linear.ResolveStates(team, p.Config.Linear.States)
	if err != nil {
		return nil, e.pause(ctx, p, err)
	}
	return &Linear{Client: c, Team: team, States: st}, nil
}

func (e *Env) pause(ctx context.Context, p *Project, cause error) error {
	return e.setPaused(ctx, p, cause, false)
}

// pauseNotion pauses the project over Notion access. Only a pickup's
// context refresh can clear it (DESIGN §6.2).
func (e *Env) pauseNotion(ctx context.Context, p *Project, cause error) error {
	return e.setPaused(ctx, p, cause, true)
}

func (e *Env) setPaused(ctx context.Context, p *Project, cause error, notion bool) error {
	reason := e.Redactor.Redact(cause.Error())
	changed, err := e.Store.SetProjectState(ctx, store.ProjectState{
		Project: p.Name, Status: store.StatusPaused, Reason: reason, Notion: notion, Commit: p.Commit,
	})
	if err != nil {
		e.Log.Error("record project state", "project", p.Name, "err", err)
	}
	if changed {
		e.Log.Warn("project paused", "project", p.Name, "commit", p.Commit, "reason", reason)
		e.Notify.Send(notify.Event{Project: p.Name, Text: fmt.Sprintf("paused at %.7s: %s", p.Commit, reason)})
	}
	return &PausedError{Project: p.Name, Commit: p.Commit, Err: cause}
}

// pausedForNotion reports whether the project is paused over Notion access.
func (e *Env) pausedForNotion(ctx context.Context, p *Project) bool {
	ps, _ := e.Store.ProjectState(ctx, p.Name)
	return ps != nil && ps.Status == store.StatusPaused && ps.Notion
}

// resume marks the project active once its config, Linear and Notion
// checks all pass.
func (e *Env) resume(ctx context.Context, p *Project) {
	changed, err := e.Store.SetProjectState(ctx, store.ProjectState{
		Project: p.Name, Status: store.StatusActive, Commit: p.Commit,
	})
	if err != nil {
		e.Log.Error("record project state", "project", p.Name, "err", err)
	}
	if changed {
		e.Log.Info("project resumed", "project", p.Name, "commit", p.Commit)
		e.Notify.Send(notify.Event{Project: p.Name, Text: fmt.Sprintf("checks pass again at %.7s · resumed", p.Commit)})
	}
}
