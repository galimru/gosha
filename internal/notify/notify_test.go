package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type redactor struct{}

func (redactor) Redact(s string) string { return strings.ReplaceAll(s, "s3cret", "***") }

func TestRender(t *testing.T) {
	got := Render(Event{Project: "next<desk>", Text: "NXT-12 · a < b & c", Links: []Link{{"PR #1", "https://x/?a=1&b=2"}}})
	want := "<b>next&lt;desk&gt;</b> · NXT-12 · a &lt; b &amp; c\n<a href=\"https://x/?a=1&amp;b=2\">PR #1</a>"
	if got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
	long := Render(Event{Text: strings.Repeat("é", maxText+10)})
	if !strings.HasSuffix(long, "…") || len([]rune(long)) != maxText+1 {
		t.Errorf("not truncated: %d runes", len([]rune(long)))
	}
}

func TestTelegramRetryAndFlush(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTOKEN/sendMessage" {
			t.Errorf("path = %s", r.URL.Path)
		}
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"ok":false,"parameters":{"retry_after":1}}`))
			return
		case 2:
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var body struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
			Mode   string `json:"parse_mode"`
		}
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &body)
		if body.ChatID != -100 || body.Mode != "HTML" {
			t.Errorf("body = %s", data)
		}
		mu.Lock()
		texts = append(texts, body.Text)
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := NewTelegram("TOKEN", -100, redactor{}, slog.New(slog.DiscardHandler), WithAPIURL(srv.URL), WithRetry(10*time.Millisecond, time.Hour))
	defer tg.Close()
	start := time.Now()
	tg.Send(Event{Project: "p", Text: "token s3cret here"})
	tg.Send(Event{Project: "p", Text: "second"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tg.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Second {
		t.Error("retry_after not honoured")
	}
	if len(texts) != 2 || texts[0] != "<b>p</b> · token *** here" {
		t.Errorf("texts = %q", texts)
	}
}

func TestTelegramDrops(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(readText(r), "bad") {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// A 400 is dropped at once; a 5xx is retried until it's older than maxAge.
	tg := NewTelegram("T", 1, nil, slog.New(slog.DiscardHandler), WithAPIURL(srv.URL), WithRetry(20*time.Millisecond, 100*time.Millisecond))
	defer tg.Close()
	tg.Send(Event{Text: "bad"})
	tg.Send(Event{Text: "down"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tg.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n < 3 || n > 6 {
		t.Errorf("calls = %d, want 1 for the 400 plus a few retries", n)
	}
}

func readText(r *http.Request) string {
	var body struct{ Text string }
	json.NewDecoder(r.Body).Decode(&body)
	return body.Text
}
