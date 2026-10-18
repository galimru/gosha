// Package github talks to GitHub as the Gosha GitHub App (DESIGN §8.6).
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v88/github"

	"github.com/galimru/gosha/internal/gitx"
)

// ErrNotInstalled means the App has no installation on the repo.
var ErrNotInstalled = errors.New("the Gosha GitHub App is not installed on this repository")

// App authenticates as the GitHub App with a JWT signed by its private key.
type App struct {
	atr     *ghinstallation.AppsTransport
	api     *gh.Client
	baseURL string
}

// Option configures an App.
type Option func(*options)

type options struct {
	baseURL string
	tr      http.RoundTripper
}

// WithBaseURL points the client at another API root (tests).
func WithBaseURL(u string) Option { return func(o *options) { o.baseURL = strings.TrimRight(u, "/") } }

// NewApp loads the private key and prepares App-level API access.
func NewApp(appID int64, privateKeyFile string, opts ...Option) (*App, error) {
	o := options{baseURL: "https://api.github.com", tr: http.DefaultTransport}
	for _, f := range opts {
		f(&o)
	}
	atr, err := ghinstallation.NewAppsTransportKeyFromFile(o.tr, appID, privateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("github app key: %w", err)
	}
	atr.BaseURL = o.baseURL
	base := o.baseURL + "/"
	api, err := gh.NewClient(gh.WithTransport(atr), gh.WithURLs(&base, nil))
	if err != nil {
		return nil, err
	}
	return &App{atr: atr, api: api, baseURL: o.baseURL}, nil
}

// AppInfo is who Gosha acts as on GitHub.
type AppInfo struct {
	ID   int64
	Slug string
	Name string
}

// BotLogin is the account PRs and commits show up as, e.g. "gosha[bot]".
func (i AppInfo) BotLogin() string { return i.Slug + "[bot]" }

// Info returns the authenticated App.
func (a *App) Info(ctx context.Context) (*AppInfo, error) {
	app, _, err := a.api.Apps.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("github app: %w", err)
	}
	return &AppInfo{ID: app.GetID(), Slug: app.GetSlug(), Name: app.GetName()}, nil
}

// Installation finds the App's installation on a repo.
func (a *App) Installation(ctx context.Context, repo gitx.GitHubRepo) (*Installation, error) {
	inst, resp, err := a.api.Apps.GetRepositoryInstallation(ctx, repo.Owner, repo.Name)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s: %w", repo, ErrNotInstalled)
	}
	if err != nil {
		return nil, fmt.Errorf("github installation for %s: %w", repo, err)
	}
	tr := ghinstallation.NewFromAppsTransport(a.atr, inst.GetID())
	tr.BaseURL = a.baseURL
	base := a.baseURL + "/"
	api, err := gh.NewClient(gh.WithTransport(tr), gh.WithURLs(&base, nil))
	if err != nil {
		return nil, err
	}
	return &Installation{ID: inst.GetID(), Repo: repo, tr: tr, api: api}, nil
}

// Installation acts on one repo with installation tokens. Tokens last 1h and
// are cached and refreshed by ghinstallation.
type Installation struct {
	ID   int64
	Repo gitx.GitHubRepo
	tr   *ghinstallation.Transport
	api  *gh.Client
}

// Token returns a valid installation token, for git over HTTPS.
func (i *Installation) Token(ctx context.Context) (string, error) {
	tok, err := i.tr.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("github installation token for %s: %w", i.Repo, err)
	}
	return tok, nil
}
