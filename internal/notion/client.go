// Package notion mirrors a Notion page tree into a markdown file tree for
// agents to read (DESIGN §6). Gosha only reads from Notion.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAPIURL = "https://api.notion.com/v1"
	// Version is the pinned Notion API version.
	Version    = "2025-09-03"
	maxRetries = 4
	// interval keeps requests under Notion's ~3 requests per second.
	interval = 350 * time.Millisecond
)

// ErrNoAccess means the token is invalid or the page isn't shared with the
// integration: a setup problem, not an outage.
var ErrNoAccess = errors.New("notion: no access")

// Client is a read-only Notion API client. It is safe for concurrent use.
type Client struct {
	token   string
	apiURL  string
	http    *http.Client
	backoff time.Duration
	gap     time.Duration

	mu   sync.Mutex
	next time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithURL points the client at another endpoint (tests).
func WithURL(u string) Option { return func(c *Client) { c.apiURL = strings.TrimRight(u, "/") } }

// WithBackoff sets the base retry delay and drops the request spacing (tests).
func WithBackoff(d time.Duration) Option {
	return func(c *Client) { c.backoff, c.gap = d, 0 }
}

// New returns a client for an internal integration's token.
func New(token string, opts ...Option) *Client {
	c := &Client{
		token: token, apiURL: defaultAPIURL,
		http:    &http.Client{Timeout: 30 * time.Second},
		backoff: time.Second, gap: interval,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Error is a failed API call.
type Error struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
	// RetryAfter is the server's Retry-After, if any.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("notion: HTTP %d", e.Status)
	}
	return fmt.Sprintf("notion: %s (%s)", e.Message, e.Code)
}

// Is makes auth failures and missing objects match ErrNoAccess. Notion
// answers object_not_found for pages that aren't shared with the
// integration.
func (e *Error) Is(target error) bool {
	return target == ErrNoAccess && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden ||
		e.Code == "unauthorized" || e.Code == "restricted_resource" || e.Code == "object_not_found")
}

func (e *Error) retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// wait spaces requests out to stay under the rate limit.
func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	at := c.next
	if at.Before(now) {
		at = now
	}
	c.next = at.Add(c.gap)
	c.mu.Unlock()
	if d := at.Sub(now); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	return nil
}

// do runs a request, retrying rate limits and server errors with backoff.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		err := c.send(ctx, method, path, payload, out)
		var nerr *Error
		if !errors.As(err, &nerr) || !nerr.retryable() || attempt >= maxRetries {
			return err
		}
		d := c.backoff << attempt
		if nerr.RetryAfter > 0 {
			d = nerr.RetryAfter
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
}

func (c *Client) send(ctx context.Context, method, path string, payload []byte, out any) error {
	if err := c.wait(ctx); err != nil {
		return err
	}
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", Version)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("notion: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		e := &Error{Status: resp.StatusCode}
		_ = json.Unmarshal(data, e)
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			e.RetryAfter = time.Duration(s) * time.Second
		}
		return e
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("notion: decode response: %w", err)
	}
	return nil
}

// RichText is one span of Notion rich text.
type RichText struct {
	Type        string `json:"type"`
	PlainText   string `json:"plain_text"`
	Href        string `json:"href"`
	Annotations struct {
		Bold          bool `json:"bold"`
		Italic        bool `json:"italic"`
		Strikethrough bool `json:"strikethrough"`
		Code          bool `json:"code"`
	} `json:"annotations"`
	Mention *struct {
		Type string `json:"type"`
		Page *struct {
			ID string `json:"id"`
		} `json:"page"`
	} `json:"mention"`
}

// Icon is a page icon; only emoji icons are kept.
type Icon struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji"`
}

// Page is a Notion page.
type Page struct {
	ID             string    `json:"id"`
	URL            string    `json:"url"`
	LastEditedTime time.Time `json:"last_edited_time"`
	InTrash        bool      `json:"in_trash"`
	Archived       bool      `json:"archived"`
	Icon           *Icon     `json:"icon"`
	Properties     map[string]struct {
		Type  string     `json:"type"`
		Title []RichText `json:"title"`
	} `json:"properties"`
}

// Title is the page's title as plain text.
func (p *Page) Title() string {
	for _, prop := range p.Properties {
		if prop.Type == "title" {
			return plain(prop.Title)
		}
	}
	return ""
}

// Emoji is the page's emoji icon, if it has one.
func (p *Page) Emoji() string {
	if p.Icon != nil && p.Icon.Type == "emoji" {
		return p.Icon.Emoji
	}
	return ""
}

// Gone reports a page in the trash.
func (p *Page) Gone() bool { return p.InTrash || p.Archived }

// Block is a Notion block. Data is the type-specific object (the value
// under the key named by Type); Children is filled in by Blocks.
type Block struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	HasChildren bool   `json:"has_children"`
	Data        json.RawMessage
	Children    []Block
}

func (b *Block) UnmarshalJSON(data []byte) error {
	var head struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		HasChildren bool   `json:"has_children"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	*b = Block{ID: head.ID, Type: head.Type, HasChildren: head.HasChildren, Data: all[head.Type]}
	return nil
}

// list is one page of a paginated response.
type list[T any] struct {
	Results    []T    `json:"results"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// Page returns a page's properties.
func (c *Client) Page(ctx context.Context, id string) (*Page, error) {
	var p Page
	if err := c.do(ctx, http.MethodGet, "/pages/"+id, nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Children returns a block's direct children, all pages of them.
func (c *Client) Children(ctx context.Context, id string) ([]Block, error) {
	var out []Block
	cursor := ""
	for {
		path := "/blocks/" + id + "/children?page_size=100"
		if cursor != "" {
			path += "&start_cursor=" + cursor
		}
		var l list[Block]
		if err := c.do(ctx, http.MethodGet, path, nil, &l); err != nil {
			return nil, err
		}
		out = append(out, l.Results...)
		if !l.HasMore {
			return out, nil
		}
		cursor = l.NextCursor
	}
}

// Search returns every page shared with the integration.
func (c *Client) Search(ctx context.Context) ([]Page, error) {
	var out []Page
	cursor := ""
	for {
		body := map[string]any{"filter": map[string]string{"property": "object", "value": "page"}, "page_size": 100}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		var l list[Page]
		if err := c.do(ctx, http.MethodPost, "/search", body, &l); err != nil {
			return nil, err
		}
		out = append(out, l.Results...)
		if !l.HasMore {
			return out, nil
		}
		cursor = l.NextCursor
	}
}

// Bot is the integration's own user.
type Bot struct {
	Name string `json:"name"`
	Bot  struct {
		WorkspaceName string `json:"workspace_name"`
	} `json:"bot"`
}

// Me returns the integration's bot user.
func (c *Client) Me(ctx context.Context) (*Bot, error) {
	var b Bot
	if err := c.do(ctx, http.MethodGet, "/users/me", nil, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

var idRe = regexp.MustCompile(`(?i)([0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12})(?:[?#].*)?$`)

// ParsePageID returns the page id in a Notion URL or a raw (dashed or
// undashed) id, as 32 lowercase hex characters.
func ParsePageID(s string) (string, error) {
	m := idRe.FindStringSubmatch(strings.TrimRight(strings.TrimSpace(s), "/"))
	if m == nil {
		return "", fmt.Errorf("no Notion page id in %q", s)
	}
	return normID(m[1]), nil
}

// normID drops dashes and lowercases an id.
func normID(id string) string { return strings.ToLower(strings.ReplaceAll(id, "-", "")) }
