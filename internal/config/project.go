package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/galimru/gosha/internal/gitx"
	"github.com/galimru/gosha/internal/notion"
	"github.com/galimru/gosha/internal/tmpl"
)

// ProjectFile is where a project's config lives in its repo (DESIGN §5.2).
const ProjectFile = ".gosha/config.yaml"

var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Project is a project's .gosha/config.yaml.
type Project struct {
	Name   string       `yaml:"name"`
	Linear Linear       `yaml:"linear"`
	Notion Notion       `yaml:"notion"`
	GitHub GitHub       `yaml:"github"`
	Review Review       `yaml:"review"`
	Checks Checks       `yaml:"checks"`
	Deploy Deploy       `yaml:"deploy"`
	Agent  ProjectAgent `yaml:"agent"`

	// Commit is the default-branch commit the config was read from.
	Commit string `yaml:"-"`
}

type Linear struct {
	Team   string `yaml:"team"`
	States States `yaml:"states"`
}

// States maps Gosha's states to the team's state names (DESIGN §3).
type States struct {
	Ready      string `yaml:"ready" default:"Ready"`
	InProgress string `yaml:"in_progress" default:"In Progress"`
	InReview   string `yaml:"in_review" default:"In Review"`
	Blocked    string `yaml:"blocked" default:"Blocked"`
}

type Notion struct {
	RootPage string `yaml:"root_page"`
}

type GitHub struct {
	DefaultBranch string `yaml:"default_branch"`
	MergeMethod   string `yaml:"merge_method" default:"squash"`
	Branch        string `yaml:"branch" default:"gosha/{{.Ticket.ID}}-{{.Ticket.Title | slug}}"`
}

type Review struct {
	MaxRounds int `yaml:"max_rounds"`
}

type Checks struct {
	Required   []string      `yaml:"required"`
	StartGrace time.Duration `yaml:"start_grace"`
	Timeout    time.Duration `yaml:"timeout"`
	MaxRounds  int           `yaml:"max_rounds"`
}

type Deploy struct {
	Workflow string        `yaml:"workflow"`
	URL      string        `yaml:"url"`
	Timeout  time.Duration `yaml:"timeout"`
}

type ProjectAgent struct {
	Timeout        time.Duration `yaml:"timeout"`
	Model          string        `yaml:"model"`
	Setup          []string      `yaml:"setup"`
	PermissionMode string        `yaml:"permission_mode" default:"dontask"`
}

// SampleTicket is what `validate` renders the branch pattern against.
var SampleTicket = tmpl.Ticket{ID: "NXT-12", Team: "NXT", Number: 12, Title: "Add login"}

// ParseProject decodes and validates a project config read from commit.
// It returns warnings that don't stop the project.
func ParseProject(data []byte, commit string) (*Project, []string, error) {
	file := ProjectFile
	if commit != "" {
		file = fmt.Sprintf("%s@%.7s", ProjectFile, commit)
	}
	p := &Project{}
	if err := decode(file, data, p); err != nil {
		return nil, nil, err
	}
	p.Commit = commit
	setDefault(&p.Review.MaxRounds, 3)
	setDefault(&p.Checks.MaxRounds, 3)
	setDefault(&p.Checks.StartGrace, 3*time.Minute)
	setDefault(&p.Checks.Timeout, 30*time.Minute)
	setDefault(&p.Agent.Timeout, 60*time.Minute)
	if p.Deploy.Workflow != "" {
		setDefault(&p.Deploy.Timeout, 20*time.Minute)
	}

	fe := func(field, msg string) error { return &FieldError{File: file, Field: field, Err: errors.New(msg)} }
	switch {
	case p.Name == "":
		return nil, nil, fe("name", "required")
	case !projectName.MatchString(p.Name):
		return nil, nil, fe("name", "must be lowercase letters, digits, '-' or '_'")
	case p.Linear.Team == "":
		return nil, nil, fe("linear.team", "required")
	case p.Notion.RootPage == "":
		return nil, nil, fe("notion.root_page", "required")
	case p.Review.MaxRounds < 1:
		return nil, nil, fe("review.max_rounds", "must be at least 1")
	case p.Checks.MaxRounds < 1:
		return nil, nil, fe("checks.max_rounds", "must be at least 1")
	}
	switch p.GitHub.MergeMethod {
	case "squash", "merge", "rebase":
	default:
		return nil, nil, fe("github.merge_method", "must be squash, merge or rebase")
	}
	switch p.Agent.PermissionMode {
	case "dontask", "auto", "bypass":
	default:
		return nil, nil, fe("agent.permission_mode", "must be dontask, auto or bypass")
	}
	if p.Deploy.URL != "" && p.Deploy.Workflow == "" {
		return nil, nil, fe("deploy.workflow", "required when deploy.url is set")
	}
	if _, err := notion.ParsePageID(p.Notion.RootPage); err != nil {
		return nil, nil, fe("notion.root_page", "must be a Notion page URL or id")
	}
	if _, err := p.BranchName(SampleTicket); err != nil {
		return nil, nil, fe("github.branch", err.Error())
	}

	var warns []string
	if !strings.Contains(p.GitHub.Branch, ".Ticket.ID") {
		warns = append(warns, "github.branch doesn't include .Ticket.ID: Linear's GitHub integration won't auto-link branches")
	}
	if p.Agent.PermissionMode == "bypass" {
		warns = append(warns, "agent.permission_mode is bypass: file tools can write anywhere the service user can")
	}
	return p, warns, nil
}

// BranchName renders the branch pattern for a ticket and checks the result
// is a valid git branch name.
func (p *Project) BranchName(t tmpl.Ticket) (string, error) {
	name, err := tmpl.Render("github.branch", p.GitHub.Branch, tmpl.Data{Project: p.Name, Ticket: t})
	if err != nil {
		return "", err
	}
	if err := gitx.CheckBranchName(name); err != nil {
		return "", err
	}
	return name, nil
}

func setDefault[T comparable](v *T, def T) {
	var zero T
	if *v == zero {
		*v = def
	}
}
