package github

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v88/github"
)

// ReviewComment is an inline comment for CreateReview. Line is in the new
// version of the file; 0 means the file as a whole.
type ReviewComment struct {
	Path string
	Line int
	Body string
}

// CreateReview posts a COMMENT review on sha (DESIGN §8.6: an App can't
// approve its own PR). Comments on lines GitHub can't anchor (outside the
// diff, or on a whole file) go into the body instead, so one bad line
// number doesn't fail the whole review.
func (i *Installation) CreateReview(ctx context.Context, number int, sha, body string, comments []ReviewComment) (int64, error) {
	lines, err := i.commentableLines(ctx, number)
	if err != nil {
		return 0, err
	}
	var inline []*gh.DraftReviewComment
	var other []string
	for _, c := range comments {
		if c.Line > 0 && lines[c.Path][c.Line] {
			inline = append(inline, &gh.DraftReviewComment{
				Path: gh.Ptr(c.Path), Line: gh.Ptr(c.Line), Side: gh.Ptr("RIGHT"), Body: gh.Ptr(c.Body),
			})
			continue
		}
		loc := "`" + c.Path + "`"
		if c.Line > 0 {
			loc = fmt.Sprintf("`%s:%d`", c.Path, c.Line)
		}
		other = append(other, "- "+loc+": "+strings.ReplaceAll(strings.TrimSpace(c.Body), "\n", "\n  "))
	}
	if len(other) > 0 {
		body += "\n\n**Other comments**\n\n" + strings.Join(other, "\n")
	}
	r, _, err := i.api.PullRequests.CreateReview(ctx, i.Repo.Owner, i.Repo.Name, number, &gh.PullRequestReviewRequest{
		CommitID: gh.Ptr(sha), Body: gh.Ptr(body), Event: gh.Ptr("COMMENT"), Comments: inline,
	})
	if err != nil {
		return 0, fmt.Errorf("github review on PR #%d: %w", number, err)
	}
	return r.GetID(), nil
}

// commentableLines maps each changed file to the new-side lines a review
// comment can be anchored to: added and context lines inside its hunks.
func (i *Installation) commentableLines(ctx context.Context, number int) (map[string]map[int]bool, error) {
	out := map[string]map[int]bool{}
	for f, err := range i.api.PullRequests.ListFilesIter(ctx, i.Repo.Owner, i.Repo.Name, number, nil) {
		if err != nil {
			return nil, fmt.Errorf("github files of PR #%d: %w", number, err)
		}
		out[f.GetFilename()] = PatchLines(f.GetPatch())
	}
	return out, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// PatchLines returns the new-side line numbers of a unified diff patch that
// are added or context lines.
func PatchLines(patch string) map[int]bool {
	lines := map[int]bool{}
	n := 0
	for _, l := range strings.Split(patch, "\n") {
		if m := hunkHeader.FindStringSubmatch(l); m != nil {
			n, _ = strconv.Atoi(m[1])
			continue
		}
		if n == 0 || l == "" {
			continue
		}
		switch l[0] {
		case '+', ' ':
			lines[n] = true
			n++
		}
	}
	return lines
}

// CreateComment posts a comment on the PR's conversation.
func (i *Installation) CreateComment(ctx context.Context, number int, body string) error {
	_, _, err := i.api.Issues.CreateComment(ctx, i.Repo.Owner, i.Repo.Name, number, &gh.IssueComment{Body: gh.Ptr(body)})
	if err != nil {
		return fmt.Errorf("github comment on PR #%d: %w", number, err)
	}
	return nil
}
