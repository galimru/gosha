package agent

import (
	"context"
	"embed"
	"errors"

	"github.com/galimru/gosha/internal/gitx"
	"github.com/galimru/gosha/internal/tmpl"
)

//go:embed prompts/*.md
var defaults embed.FS

// PromptDir is where a repo overrides the default prompts (DESIGN §5.3).
const PromptDir = ".gosha/prompts/"

// LoadPrompt reads a prompt template from the repo at commit (the default
// branch, never a checkout), falling back to the embedded default.
func LoadPrompt(ctx context.Context, repo *gitx.Repo, commit, name string) (string, error) {
	data, err := repo.Show(ctx, commit, PromptDir+name)
	if errors.Is(err, gitx.ErrNotFound) {
		data, err = defaults.ReadFile("prompts/" + name)
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// RenderPrompt loads and renders a prompt template.
func RenderPrompt(ctx context.Context, repo *gitx.Repo, commit, name string, d tmpl.Data) (string, error) {
	text, err := LoadPrompt(ctx, repo, commit, name)
	if err != nil {
		return "", err
	}
	return tmpl.Render(PromptDir+name, text, d)
}
