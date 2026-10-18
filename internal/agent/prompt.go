package agent

import (
	"context"
	"embed"
	"errors"
	"strings"

	"github.com/galimru/gosha/internal/gitx"
	"github.com/galimru/gosha/internal/tmpl"
)

//go:embed prompts/*.md
var prompts embed.FS

// InstructionsFile is a repo's optional extra instructions, appended to
// every prompt (DESIGN §5.3).
const InstructionsFile = ".gosha/instructions.md"

// RenderPrompt renders a built-in prompt and appends the repo's
// instructions file, read at commit (the default branch, never a checkout).
func RenderPrompt(ctx context.Context, repo *gitx.Repo, commit, name string, d tmpl.Data) (string, error) {
	text, err := prompts.ReadFile("prompts/" + name)
	if err != nil {
		return "", err
	}
	out, err := tmpl.Render(name, string(text), d)
	if err != nil {
		return "", err
	}
	extra, err := repo.Show(ctx, commit, InstructionsFile)
	if errors.Is(err, gitx.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return "", err
	}
	if s := strings.TrimSpace(string(extra)); s != "" {
		out = strings.TrimRight(out, "\n") + "\n\n# Project instructions\n\nFrom the repository's `" + InstructionsFile + "`:\n\n" + s + "\n"
	}
	return out, nil
}
