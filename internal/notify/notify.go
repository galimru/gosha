// Package notify posts Gosha's activity feed to a Telegram channel
// (DESIGN §9). Sending is asynchronous and never blocks the pipeline.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Event is one activity feed message.
type Event struct {
	Project string
	Text    string // plain text, may span lines; escaped when rendered
	Links   []Link
}

// Link is a labelled URL shown under the text.
type Link struct{ Text, URL string }

// Notifier accepts events. Send must not block.
type Notifier interface {
	Send(Event)
	// Flush waits until every queued event is sent or dropped, or ctx ends.
	Flush(ctx context.Context) error
}

// Nop drops every event.
type Nop struct{}

func (Nop) Send(Event)                  {}
func (Nop) Flush(context.Context) error { return nil }

// Redactor scrubs secrets from outgoing text.
type Redactor interface{ Redact(string) string }

// maxText keeps messages well under Telegram's 4096-character limit and
// short enough to read on a phone.
const maxText = 3000

// Render formats an event as Telegram HTML.
func Render(ev Event) string {
	text := ev.Text
	if utf8.RuneCountInString(text) > maxText {
		text = string([]rune(text)[:maxText]) + "…"
	}
	var b strings.Builder
	if ev.Project != "" {
		b.WriteString("<b>" + html.EscapeString(ev.Project) + "</b> · ")
	}
	b.WriteString(html.EscapeString(text))
	for _, l := range ev.Links {
		fmt.Fprintf(&b, "\n<a href=\"%s\">%s</a>", html.EscapeString(l.URL), html.EscapeString(l.Text))
	}
	return b.String()
}

// Telegram sends events through the Bot API from a background worker.
type Telegram struct {
	token   string
	chatID  int64
	apiURL  string
	http    *http.Client
	red     Redactor
	log     *slog.Logger
	maxAge  time.Duration
	backoff time.Duration

	queue   chan item
	pending sync.WaitGroup
	done    chan struct{}
}

type item struct {
	text string
	at   time.Time
}

// Option configures Telegram.
type Option func(*Telegram)

// WithAPIURL points the sender at another Bot API root (tests).
func WithAPIURL(u string) Option { return func(t *Telegram) { t.apiURL = strings.TrimRight(u, "/") } }

// WithRetry sets the base backoff and the age after which events are dropped.
func WithRetry(backoff, maxAge time.Duration) Option {
	return func(t *Telegram) { t.backoff, t.maxAge = backoff, maxAge }
}

// NewTelegram starts the sender's worker.
func NewTelegram(token string, chatID int64, red Redactor, log *slog.Logger, opts ...Option) *Telegram {
	t := &Telegram{
		token: token, chatID: chatID, red: red, log: log,
		apiURL:  "https://api.telegram.org",
		http:    &http.Client{Timeout: 20 * time.Second},
		maxAge:  time.Hour,
		backoff: time.Second,
		queue:   make(chan item, 256),
		done:    make(chan struct{}),
	}
	for _, o := range opts {
		o(t)
	}
	go t.worker()
	return t
}

// Send queues an event. If the queue is full the event is dropped and logged.
func (t *Telegram) Send(ev Event) {
	text := Render(ev)
	if t.red != nil {
		text = t.red.Redact(text)
	}
	t.pending.Add(1)
	select {
	case t.queue <- item{text: text, at: time.Now()}:
	default:
		t.pending.Done()
		t.log.Warn("telegram queue full, event dropped", "project", ev.Project)
	}
}

// Flush waits for the queue to drain.
func (t *Telegram) Flush(ctx context.Context) error {
	ch := make(chan struct{})
	go func() { t.pending.Wait(); close(ch) }()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("telegram: %d message(s) not sent: %w", len(t.queue), ctx.Err())
	}
}

// Close stops the worker; queued events are abandoned.
func (t *Telegram) Close() { close(t.done) }

func (t *Telegram) worker() {
	for {
		select {
		case <-t.done:
			return
		case it := <-t.queue:
			t.deliver(it)
			t.pending.Done()
		}
	}
}

func (t *Telegram) deliver(it item) {
	delay := t.backoff
	for {
		wait, err := t.post(it.text)
		if err == nil {
			return
		}
		if wait < 0 {
			t.log.Error("telegram message rejected, dropped", "err", err)
			return
		}
		if wait == 0 {
			wait = delay
			delay = min(delay*2, time.Minute)
		}
		if time.Since(it.at)+wait > t.maxAge {
			t.log.Error("telegram unreachable, message dropped", "err", err, "age", time.Since(it.at).Round(time.Second))
			return
		}
		t.log.Warn("telegram send failed, retrying", "err", err, "in", wait)
		select {
		case <-t.done:
			return
		case <-time.After(wait):
		}
	}
}

// post sends one message. On failure it returns how long to wait before
// retrying: 0 for the default backoff, a value from retry_after, or -1 if
// retrying can't help.
func (t *Telegram) post(text string) (time.Duration, error) {
	body, _ := json.Marshal(map[string]any{
		"chat_id":              t.chatID,
		"text":                 text,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
	})
	resp, err := t.http.Post(t.apiURL+"/bot"+t.token+"/sendMessage", "application/json", bytes.NewReader(body))
	if err != nil {
		// The URL contains the token; keep it out of logs.
		return 0, fmt.Errorf("telegram: %s", strings.ReplaceAll(err.Error(), t.token, "***"))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	_ = json.Unmarshal(data, &r)
	switch {
	case resp.StatusCode == http.StatusOK && r.OK:
		return 0, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return time.Duration(max(r.Parameters.RetryAfter, 1)) * time.Second, fmt.Errorf("telegram: rate limited")
	case resp.StatusCode >= 500:
		return 0, fmt.Errorf("telegram: %s", resp.Status)
	default:
		return -1, fmt.Errorf("telegram: %s: %s", resp.Status, r.Description)
	}
}
