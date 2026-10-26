package github

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	gh "github.com/google/go-github/v88/github"
)

// Permissions are the repository permissions the Gosha App needs (DESIGN
// §8.6). Workflows is left out on purpose: without it GitHub refuses any
// push that changes .github/workflows/, so agents can't change CI.
var Permissions = map[string]string{
	"contents":      "write",
	"pull_requests": "write",
	"checks":        "read",
	"actions":       "read",
	"statuses":      "read",
	"metadata":      "read",
}

// PermissionCheck compares an App's permissions with Permissions.
type PermissionCheck struct {
	// Missing are required permissions the App lacks or has only read
	// access to, as "name: level".
	Missing []string
	// Extra are permissions Gosha doesn't need.
	Extra []string
	// Workflows means the App may change workflow files.
	Workflows bool
}

// CheckPermissions compares granted permissions with Permissions.
func CheckPermissions(granted map[string]string) PermissionCheck {
	var c PermissionCheck
	for name, want := range Permissions {
		if got := granted[name]; got != want && got != "admin" && !(want == "read" && got == "write") {
			c.Missing = append(c.Missing, name+": "+want)
		}
	}
	for name, level := range granted {
		if _, ok := Permissions[name]; ok || level == "" {
			continue
		}
		if name == "workflows" {
			c.Workflows = true
			continue
		}
		c.Extra = append(c.Extra, name+": "+level)
	}
	slices.Sort(c.Missing)
	slices.Sort(c.Extra)
	return c
}

// Manifest is a GitHub App manifest for Gosha's App: Gosha's permissions,
// no webhook, private. GitHub's form lets the operator change the name.
func Manifest(name, redirectURL string) map[string]any {
	return map[string]any{
		"name":        name,
		"url":         "https://github.com/galimru/gosha",
		"description": "Gosha turns Ready Linear tickets into merged pull requests.",
		// GitHub requires a hook URL even for an inactive webhook.
		"hook_attributes":     map[string]any{"url": "https://github.com/galimru/gosha", "active": false},
		"redirect_url":        redirectURL,
		"public":              false,
		"default_permissions": Permissions,
		"default_events":      []string{},
	}
}

// NewAppConfig is an App created from a manifest.
type NewAppConfig struct {
	ID   int64
	Slug string
	Name string
	PEM  string
}

// ConvertManifest exchanges the one-time code GitHub redirects back with
// for the new App's id and private key. The call needs no auth.
func ConvertManifest(ctx context.Context, code string, opts ...Option) (*NewAppConfig, error) {
	o := options{baseURL: "https://api.github.com", tr: http.DefaultTransport}
	for _, f := range opts {
		f(&o)
	}
	base := o.baseURL + "/"
	api, err := gh.NewClient(gh.WithTransport(o.tr), gh.WithURLs(&base, nil))
	if err != nil {
		return nil, err
	}
	cfg, _, err := api.Apps.CompleteAppManifest(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("github app manifest: %w", err)
	}
	if cfg.GetID() == 0 || strings.TrimSpace(cfg.GetPEM()) == "" {
		return nil, fmt.Errorf("github app manifest: no app id or key in the response")
	}
	return &NewAppConfig{ID: cfg.GetID(), Slug: cfg.GetSlug(), Name: cfg.GetName(), PEM: cfg.GetPEM()}, nil
}
