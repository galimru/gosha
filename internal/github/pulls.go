package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v88/github"
)

// Identity is the git author and committer for the bot's commits.
type Identity struct{ Name, Email string }

// BotIdentity looks up the App's bot user. Its noreply email needs the bot
// *user* id, which differs from the App id.
func (i *Installation) BotIdentity(ctx context.Context, app AppInfo) (Identity, error) {
	login := app.BotLogin()
	u, _, err := i.api.Users.Get(ctx, login)
	if err != nil {
		return Identity{}, fmt.Errorf("github user %s: %w", login, err)
	}
	return Identity{Name: login, Email: fmt.Sprintf("%d+%s@users.noreply.github.com", u.GetID(), login)}, nil
}

// PR is the part of a pull request Gosha tracks.
type PR struct {
	Number  int
	URL     string
	State   string // open or closed
	Merged  bool
	HeadSHA string
}

func toPR(p *gh.PullRequest) *PR {
	return &PR{Number: p.GetNumber(), URL: p.GetHTMLURL(), State: p.GetState(), Merged: p.GetMerged(), HeadSHA: p.GetHead().GetSHA()}
}

// PR fetches a pull request by number.
func (i *Installation) PR(ctx context.Context, number int) (*PR, error) {
	p, _, err := i.api.PullRequests.Get(ctx, i.Repo.Owner, i.Repo.Name, number)
	if err != nil {
		return nil, fmt.Errorf("github PR #%d: %w", number, err)
	}
	return toPR(p), nil
}

// OpenPR returns the open pull request whose head is branch, or nil.
func (i *Installation) OpenPR(ctx context.Context, branch string) (*PR, error) {
	ps, _, err := i.api.PullRequests.List(ctx, i.Repo.Owner, i.Repo.Name, &gh.PullRequestListOptions{
		State: "open", Head: i.Repo.Owner + ":" + branch,
	})
	if err != nil {
		return nil, fmt.Errorf("github PRs for %s: %w", branch, err)
	}
	if len(ps) == 0 {
		return nil, nil
	}
	return toPR(ps[0]), nil
}

// CreatePR opens a pull request from branch into base, or returns the one
// that's already open for branch, so repeating it is safe.
func (i *Installation) CreatePR(ctx context.Context, branch, base, title, body string) (*PR, error) {
	if p, err := i.OpenPR(ctx, branch); err != nil || p != nil {
		return p, err
	}
	p, _, err := i.api.PullRequests.Create(ctx, i.Repo.Owner, i.Repo.Name, &gh.NewPullRequest{
		Title: gh.Ptr(title), Head: gh.Ptr(branch), Base: gh.Ptr(base), Body: gh.Ptr(body),
	})
	if err != nil {
		return nil, fmt.Errorf("github create PR for %s: %w", branch, err)
	}
	return toPR(p), nil
}

// ClosePR closes a pull request without merging it.
func (i *Installation) ClosePR(ctx context.Context, number int) error {
	_, _, err := i.api.PullRequests.Edit(ctx, i.Repo.Owner, i.Repo.Name, number, &gh.PullRequest{State: gh.Ptr("closed")})
	if err != nil {
		return fmt.Errorf("github close PR #%d: %w", number, err)
	}
	return nil
}

// ErrConflict means the base branch doesn't merge cleanly into the PR.
var ErrConflict = errors.New("the base branch conflicts with the pull request")

// UpdateBranch asks GitHub to merge the base into the PR's branch, as long
// as its head is still head. GitHub merges in the
// background: the PR's head changes once it's done. A head that moved is
// ErrHeadMoved, and a conflict ErrConflict.
func (i *Installation) UpdateBranch(ctx context.Context, number int, head string) error {
	_, resp, err := i.api.PullRequests.UpdateBranch(ctx, i.Repo.Owner, i.Repo.Name, number,
		&gh.PullRequestBranchUpdateOptions{ExpectedHeadSHA: gh.Ptr(head)})
	var accepted *gh.AcceptedError
	if errors.As(err, &accepted) {
		return nil
	}
	if resp != nil && resp.StatusCode == http.StatusUnprocessableEntity {
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "expected head sha"):
			return fmt.Errorf("github update PR #%d: %w: %v", number, ErrHeadMoved, err)
		case strings.Contains(msg, "conflict"):
			return fmt.Errorf("github update PR #%d: %w: %v", number, ErrConflict, err)
		}
	}
	if err != nil {
		return fmt.Errorf("github update PR #%d: %w", number, err)
	}
	return nil
}
