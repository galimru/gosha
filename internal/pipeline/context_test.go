package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/galimru/gosha/internal/notion"
	"github.com/galimru/gosha/internal/store"
)

const rootID = "1a2b3c4d5e6f708192a3b4c5d6e7f809"

// fakeNotion is a workspace with just the root page, or none (down,
// noAccess).
type fakeNotion struct {
	down, noAccess bool
}

func (f fakeNotion) err() error {
	if f.noAccess {
		return &notion.Error{Status: 404, Code: "object_not_found"}
	}
	if f.down {
		return &notion.Error{Status: 503, Code: "service_unavailable"}
	}
	return nil
}

func (f fakeNotion) Page(context.Context, string) (*notion.Page, error) {
	if err := f.err(); err != nil {
		return nil, err
	}
	var p notion.Page
	err := json.Unmarshal([]byte(`{"id":"`+rootID+`","url":"https://www.notion.so/`+rootID+`","last_edited_time":"2026-10-01T09:00:00Z",
		"properties":{"title":{"type":"title","title":[{"plain_text":"Login spec"}]}}}`), &p)
	return &p, err
}

func (f fakeNotion) Children(context.Context, string) ([]notion.Block, error) {
	if err := f.err(); err != nil {
		return nil, err
	}
	var bs []notion.Block
	err := json.Unmarshal([]byte(`[{"id":"p","type":"paragraph","paragraph":{"rich_text":[{"plain_text":"Sessions last 30 days."}]}}]`), &bs)
	return bs, err
}

func (f fakeNotion) Search(context.Context) ([]notion.Page, error) {
	if f.down {
		return nil, f.err()
	}
	return nil, nil
}

func TestContextReachesAgents(t *testing.T) {
	h := newHarness(t, "")
	h.env.Notion = func(string) notion.API { return fakeNotion{} }
	if err := h.env.refreshContext(h.ctx, h.p); err != nil {
		t.Fatal(err)
	}
	if ev := strings.Join(h.events.take(), "\n"); !strings.Contains(ev, "context · 1 added") {
		t.Errorf("events = %s", ev)
	}
	kb, err := h.env.freezeContext(h.p, "NXT-12")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(kb.Dir, "/workspaces/demo/NXT-12/context") || !strings.Contains(kb.Index, "[Login spec](README.md) — Sessions last 30 days.") {
		t.Fatalf("kb = %+v", kb)
	}
	h.kb = kb
	h.agent.act = func(_ int, dir string) string { commitFile(t, dir, "login.go"); return doneResult }
	if err := h.pickup(); err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct {
		role     string
		settings string
		prompt   string
		dirs     []string
	}{
		{"implementer", h.agent.calls[0].SettingsFile, h.agent.calls[0].Prompt, h.agent.calls[0].AddDirs},
		{"reviewer", h.agent.reviews[0].SettingsFile, h.agent.reviews[0].Prompt, h.agent.reviews[0].AddDirs},
	} {
		if !slices.Equal(s.dirs, []string{kb.Dir}) || !strings.Contains(s.prompt, kb.Dir) || !strings.Contains(s.prompt, "Login spec") {
			t.Errorf("%s: add dirs %v, prompt:\n%s", s.role, s.dirs, s.prompt)
		}
		settings, _ := os.ReadFile(s.settings)
		if !strings.Contains(string(settings), `"denyWrite"`) || !strings.Contains(string(settings), "Edit(/"+kb.Dir+"/**)") {
			t.Errorf("%s settings don't protect the context:\n%s", s.role, settings)
		}
	}
}

func TestContextFailures(t *testing.T) {
	h := newHarness(t, "")
	state := func() string {
		ps, _ := h.env.Store.ProjectState(h.ctx, "demo")
		if ps == nil {
			return ""
		}
		return ps.Status
	}

	// Down before any mirror exists: an error to retry, not a pause.
	h.env.Notion = func(string) notion.API { return fakeNotion{down: true} }
	var perr *PausedError
	if err := h.env.refreshContext(h.ctx, h.p); err == nil || errors.As(err, &perr) {
		t.Fatalf("down, no mirror: %v", err)
	}

	// Down with a mirror: keep using it.
	h.env.Notion = func(string) notion.API { return fakeNotion{} }
	if err := h.env.refreshContext(h.ctx, h.p); err != nil {
		t.Fatal(err)
	}
	h.env.Notion = func(string) notion.API { return fakeNotion{down: true} }
	if err := h.env.refreshContext(h.ctx, h.p); err != nil {
		t.Fatalf("down with a mirror: %v", err)
	}

	// No access: paused until a refresh works again.
	h.env.Notion = func(string) notion.API { return fakeNotion{noAccess: true} }
	if err := h.env.refreshContext(h.ctx, h.p); !errors.As(err, &perr) || !errors.Is(err, notion.ErrNoAccess) || state() != store.StatusPaused {
		t.Fatalf("no access: %v, state %q", err, state())
	}
	if !h.env.pausedForNotion(h.ctx, h.p) {
		t.Error("not paused for Notion")
	}
	// A pickup's refresh that works again lets it resume.
	h.env.Notion = func(string) notion.API { return fakeNotion{} }
	if err := h.env.refreshContext(h.ctx, h.p); err != nil {
		t.Fatal(err)
	}
	h.env.resume(h.ctx, h.p)
	if state() != store.StatusActive || h.env.pausedForNotion(h.ctx, h.p) {
		t.Errorf("after a working refresh: state %q", state())
	}
}
