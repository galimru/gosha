package config

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/galimru/gosha/internal/notion"
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
	Timeout time.Duration `yaml:"timeout"`
	Model   string        `yaml:"model"`
}

// ParseProject decodes and validates a project config read from commit.
func ParseProject(data []byte, commit string) (*Project, error) {
	file := ProjectFile
	if commit != "" {
		file = fmt.Sprintf("%s@%.7s", ProjectFile, commit)
	}
	p := &Project{}
	if err := decode(file, data, p); err != nil {
		return nil, err
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
		return nil, fe("name", "required")
	case !projectName.MatchString(p.Name):
		return nil, fe("name", "must be lowercase letters, digits, '-' or '_'")
	case p.Linear.Team == "":
		return nil, fe("linear.team", "required")
	case p.Notion.RootPage == "":
		return nil, fe("notion.root_page", "required")
	case p.Review.MaxRounds < 1:
		return nil, fe("review.max_rounds", "must be at least 1")
	case p.Checks.MaxRounds < 1:
		return nil, fe("checks.max_rounds", "must be at least 1")
	}
	switch p.GitHub.MergeMethod {
	case "squash", "merge", "rebase":
	default:
		return nil, fe("github.merge_method", "must be squash, merge or rebase")
	}
	if p.Deploy.URL != "" && p.Deploy.Workflow == "" {
		return nil, fe("deploy.workflow", "required when deploy.url is set")
	}
	if _, err := notion.ParsePageID(p.Notion.RootPage); err != nil {
		return nil, fe("notion.root_page", "must be a Notion page URL or id")
	}
	return p, nil
}

func setDefault[T comparable](v *T, def T) {
	var zero T
	if *v == zero {
		*v = def
	}
}
