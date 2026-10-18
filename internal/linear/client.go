// Package linear talks to Linear's GraphQL API as Gosha's OAuth app, using
// client_credentials tokens.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAPIURL   = "https://api.linear.app/graphql"
	defaultTokenURL = "https://api.linear.app/oauth/token"
	scopes          = "read,write,issues:create,comments:create"
	maxRetries      = 3
)

// Client is a Linear API client. It is safe for concurrent use.
type Client struct {
	clientID, clientSecret string
	apiURL, tokenURL       string
	http                   *http.Client
	backoff                time.Duration

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithURLs points the client at other endpoints (tests).
func WithURLs(api, token string) Option {
	return func(c *Client) { c.apiURL, c.tokenURL = api, token }
}

// WithBackoff sets the base retry delay (tests).
func WithBackoff(d time.Duration) Option { return func(c *Client) { c.backoff = d } }

// New returns a client for the OAuth app's credentials.
func New(clientID, clientSecret string, opts ...Option) *Client {
	c := &Client{
		clientID: clientID, clientSecret: clientSecret,
		apiURL: defaultAPIURL, tokenURL: defaultTokenURL,
		http:    &http.Client{Timeout: 30 * time.Second},
		backoff: time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// accessToken returns the cached token, minting a new one when there is
// none or it's about to expire. Tokens are kept in memory only.
func (c *Client) accessToken(ctx context.Context, renew bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !renew && c.token != "" && time.Until(c.expiry) > time.Minute {
		return c.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"scope":         {scopes},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("linear token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("linear token: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("linear token: unexpected response")
	}
	c.token = tok.AccessToken
	c.expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return c.token, nil
}

// GraphQLError is an error returned in a GraphQL response.
type GraphQLError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

// Error is a failed API call.
type Error struct {
	Status int
	Errors []GraphQLError
}

func (e *Error) Error() string {
	var msgs []string
	for _, g := range e.Errors {
		msgs = append(msgs, g.Message)
	}
	if len(msgs) == 0 {
		return fmt.Sprintf("linear: HTTP %d", e.Status)
	}
	return "linear: " + strings.Join(msgs, "; ")
}

func (e *Error) hasCode(code string) bool {
	for _, g := range e.Errors {
		if g.Extensions.Code == code {
			return true
		}
	}
	return false
}

func (e *Error) unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.hasCode("AUTHENTICATION_ERROR")
}

func (e *Error) rateLimited() bool {
	return e.Status == http.StatusTooManyRequests || e.hasCode("RATELIMITED")
}

// do runs a GraphQL operation. An auth failure re-mints the token once;
// rate limits are retried with backoff.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	renewed := false
	for attempt := 0; ; attempt++ {
		tok, err := c.accessToken(ctx, false)
		if err != nil {
			return err
		}
		err = c.post(ctx, tok, query, vars, out)
		var lerr *Error
		switch {
		case err == nil:
			return nil
		case errors.As(err, &lerr) && lerr.unauthorized() && !renewed:
			renewed = true
			if _, err := c.accessToken(ctx, true); err != nil {
				return err
			}
		case errors.As(err, &lerr) && lerr.rateLimited() && attempt < maxRetries:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.backoff << attempt):
			}
		default:
			return err
		}
	}
}

func (c *Client) post(ctx context.Context, token, query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	var r struct {
		Data   json.RawMessage `json:"data"`
		Errors []GraphQLError  `json:"errors"`
	}
	_ = json.Unmarshal(body, &r)
	if resp.StatusCode != http.StatusOK || len(r.Errors) > 0 {
		return &Error{Status: resp.StatusCode, Errors: r.Errors}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(r.Data, out); err != nil {
		return fmt.Errorf("linear: decode response: %w", err)
	}
	return nil
}

// Viewer is the actor Gosha acts as.
type Viewer struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

// Viewer returns the token's actor, normally the app user.
func (c *Client) Viewer(ctx context.Context) (*Viewer, error) {
	var out struct{ Viewer Viewer }
	if err := c.do(ctx, `query { viewer { id name displayName } }`, nil, &out); err != nil {
		return nil, err
	}
	return &out.Viewer, nil
}

// State types used by Linear.
const (
	TypeBacklog   = "backlog"
	TypeUnstarted = "unstarted"
	TypeStarted   = "started"
	TypeCompleted = "completed"
	TypeCanceled  = "canceled"
	TypeDuplicate = "duplicate"
)

// State is a workflow state.
type State struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Team is a Linear team with its workflow states.
type Team struct {
	ID     string
	Key    string
	Name   string
	States []State
}

// ErrTeamNotFound means no team has the configured key.
var ErrTeamNotFound = errors.New("linear team not found")

// Team looks up a team by key.
func (c *Client) Team(ctx context.Context, key string) (*Team, error) {
	var out struct {
		Teams struct {
			Nodes []struct {
				ID, Key, Name string
				States        struct{ Nodes []State }
			}
		}
	}
	const q = `query($key: String!) {
		teams(filter: { key: { eq: $key } }) {
			nodes { id key name states(first: 100) { nodes { id name type } } }
		}
	}`
	if err := c.do(ctx, q, map[string]any{"key": key}, &out); err != nil {
		return nil, err
	}
	if len(out.Teams.Nodes) == 0 {
		return nil, fmt.Errorf("%w: %q (or not visible to Gosha's app)", ErrTeamNotFound, key)
	}
	n := out.Teams.Nodes[0]
	return &Team{ID: n.ID, Key: n.Key, Name: n.Name, States: n.States.Nodes}, nil
}

// Blocker is an issue that blocks another.
type Blocker struct {
	Identifier string
	State      State
}

// Issue is a ticket with what pickup needs.
type Issue struct {
	ID          string
	Identifier  string
	Number      int
	Title       string
	Description string
	URL         string
	Priority    int // 0 none, 1 urgent, 2 high, 3 medium, 4 low
	CreatedAt   time.Time
	State       State
	BlockedBy   []Blocker
}

type issueNode struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Number      float64   `json:"number"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	Priority    float64   `json:"priority"`
	CreatedAt   time.Time `json:"createdAt"`
	State       State     `json:"state"`
	// inverseRelations of type "blocks" are the issues blocking this one.
	InverseRelations struct {
		Nodes []struct {
			Type  string `json:"type"`
			Issue struct {
				Identifier string `json:"identifier"`
				State      State  `json:"state"`
			} `json:"issue"`
		} `json:"nodes"`
	} `json:"inverseRelations"`
}

const issueFields = `id identifier number title description url priority createdAt
	state { id name type }
	inverseRelations(first: 50) { nodes { type issue { identifier state { id name type } } } }`

func (n issueNode) issue() Issue {
	is := Issue{
		ID: n.ID, Identifier: n.Identifier, Number: int(n.Number), Title: n.Title,
		Description: n.Description, URL: n.URL, Priority: int(n.Priority),
		CreatedAt: n.CreatedAt, State: n.State,
	}
	for _, r := range n.InverseRelations.Nodes {
		if r.Type == "blocks" {
			is.BlockedBy = append(is.BlockedBy, Blocker{Identifier: r.Issue.Identifier, State: r.Issue.State})
		}
	}
	return is
}

// Issue fetches one issue by id or identifier (e.g. "NXT-12").
func (c *Client) Issue(ctx context.Context, id string) (*Issue, error) {
	const q = `query($id: String!) { issue(id: $id) { ` + issueFields + ` } }`
	var out struct{ Issue *issueNode }
	if err := c.do(ctx, q, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Issue == nil {
		return nil, fmt.Errorf("linear: issue %s not found", id)
	}
	is := out.Issue.issue()
	return &is, nil
}

// IssuesInState lists every issue of a team in a state.
func (c *Client) IssuesInState(ctx context.Context, teamID, stateID string) ([]Issue, error) {
	const q = `query($team: ID!, $state: ID!, $after: String) {
		issues(first: 50, after: $after, filter: { team: { id: { eq: $team } }, state: { id: { eq: $state } } }) {
			nodes { ` + issueFields + ` }
			pageInfo { hasNextPage endCursor }
		}
	}`
	var all []Issue
	var after *string
	for {
		var out struct {
			Issues struct {
				Nodes    []issueNode
				PageInfo pageInfo
			}
		}
		if err := c.do(ctx, q, map[string]any{"team": teamID, "state": stateID, "after": after}, &out); err != nil {
			return nil, err
		}
		for _, n := range out.Issues.Nodes {
			all = append(all, n.issue())
		}
		if !out.Issues.PageInfo.HasNextPage {
			return all, nil
		}
		after = &out.Issues.PageInfo.EndCursor
	}
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// Comment is a ticket comment.
type Comment struct {
	ID        string
	Body      string
	CreatedAt time.Time
	Author    string
	// Human is false for Gosha's own comments and for other bots and integrations.
	Human bool
}

// Comments lists an issue's comments, oldest first.
func (c *Client) Comments(ctx context.Context, issueID string) ([]Comment, error) {
	const q = `query($id: String!, $after: String) {
		issue(id: $id) {
			comments(first: 100, after: $after) {
				nodes { id body createdAt user { name isMe } botActor { name } }
				pageInfo { hasNextPage endCursor }
			}
		}
	}`
	var all []Comment
	var after *string
	for {
		var out struct {
			Issue struct {
				Comments struct {
					Nodes []struct {
						ID        string    `json:"id"`
						Body      string    `json:"body"`
						CreatedAt time.Time `json:"createdAt"`
						User      *struct {
							Name string `json:"name"`
							IsMe bool   `json:"isMe"`
						} `json:"user"`
						BotActor *struct {
							Name string `json:"name"`
						} `json:"botActor"`
					}
					PageInfo pageInfo
				}
			}
		}
		if err := c.do(ctx, q, map[string]any{"id": issueID, "after": after}, &out); err != nil {
			return nil, err
		}
		for _, n := range out.Issue.Comments.Nodes {
			cm := Comment{ID: n.ID, Body: n.Body, CreatedAt: n.CreatedAt}
			switch {
			case n.User != nil:
				cm.Author, cm.Human = n.User.Name, !n.User.IsMe && n.BotActor == nil
			case n.BotActor != nil:
				cm.Author = n.BotActor.Name
			}
			all = append(all, cm)
		}
		if !out.Issue.Comments.PageInfo.HasNextPage {
			break
		}
		after = &out.Issue.Comments.PageInfo.EndCursor
	}
	slices.SortStableFunc(all, func(a, b Comment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return all, nil
}

func checkSuccess(op string, ok bool) error {
	if !ok {
		return fmt.Errorf("linear: %s was not successful", op)
	}
	return nil
}

// Transition moves an issue to a state.
func (c *Client) Transition(ctx context.Context, issueID, stateID string) error {
	var out struct {
		IssueUpdate struct{ Success bool } `json:"issueUpdate"`
	}
	const q = `mutation($id: String!, $state: String!) { issueUpdate(id: $id, input: { stateId: $state }) { success } }`
	if err := c.do(ctx, q, map[string]any{"id": issueID, "state": stateID}, &out); err != nil {
		return err
	}
	return checkSuccess("issueUpdate", out.IssueUpdate.Success)
}

// CreateComment posts a markdown comment and returns its id.
func (c *Client) CreateComment(ctx context.Context, issueID, body string) (string, error) {
	var out struct {
		CommentCreate struct {
			Success bool
			Comment struct{ ID string }
		} `json:"commentCreate"`
	}
	const q = `mutation($id: String!, $body: String!) { commentCreate(input: { issueId: $id, body: $body }) { success comment { id } } }`
	if err := c.do(ctx, q, map[string]any{"id": issueID, "body": body}, &out); err != nil {
		return "", err
	}
	return out.CommentCreate.Comment.ID, checkSuccess("commentCreate", out.CommentCreate.Success)
}

// CreateAttachment adds a link card (the PR) to an issue. Linear dedupes
// attachments by URL, so repeating it is safe.
func (c *Client) CreateAttachment(ctx context.Context, issueID, link, title, subtitle string) error {
	var out struct {
		AttachmentCreate struct{ Success bool } `json:"attachmentCreate"`
	}
	const q = `mutation($id: String!, $url: String!, $title: String!, $subtitle: String) {
		attachmentCreate(input: { issueId: $id, url: $url, title: $title, subtitle: $subtitle }) { success }
	}`
	if err := c.do(ctx, q, map[string]any{"id": issueID, "url": link, "title": title, "subtitle": subtitle}, &out); err != nil {
		return err
	}
	return checkSuccess("attachmentCreate", out.AttachmentCreate.Success)
}

// PriorityName is Linear's label for a priority value.
func PriorityName(p int) string {
	switch p {
	case 1:
		return "Urgent"
	case 2:
		return "High"
	case 3:
		return "Medium"
	case 4:
		return "Low"
	case 0:
		return "None"
	}
	return strconv.Itoa(p)
}
