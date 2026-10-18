package notion

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeAPI is a Notion workspace in memory.
type fakeAPI struct {
	pages     map[string]*fakePage
	hidden    map[string]bool // pages Search doesn't list (yet)
	children  []string        // ids whose children were fetched
	searchErr error
}

type fakePage struct {
	title  string
	edited time.Time
	blocks []Block
	trash  bool
}

func (f *fakeAPI) page(id string) *Page {
	p := f.pages[id]
	props := map[string]struct {
		Type  string     `json:"type"`
		Title []RichText `json:"title"`
	}{"title": {Type: "title", Title: []RichText{{Type: "text", PlainText: p.title}}}}
	return &Page{ID: id, URL: "https://www.notion.so/" + id, LastEditedTime: p.edited, InTrash: p.trash, Properties: props}
}

func (f *fakeAPI) Page(_ context.Context, id string) (*Page, error) {
	if f.pages[id] == nil {
		return nil, &Error{Status: 404, Code: "object_not_found"}
	}
	return f.page(id), nil
}

func (f *fakeAPI) Children(_ context.Context, id string) ([]Block, error) {
	f.children = append(f.children, id)
	if p := f.pages[id]; p != nil {
		return p.blocks, nil
	}
	return nil, nil
}

func (f *fakeAPI) Search(context.Context) ([]Page, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	var out []Page
	for id, p := range f.pages {
		if !f.hidden[id] && !p.trash {
			out = append(out, *f.page(id))
		}
	}
	return out, nil
}

func id(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }

func blk(typ string, data any) Block {
	raw, _ := json.Marshal(data)
	return Block{ID: "b" + typ, Type: typ, Data: raw}
}

func para(s string) Block {
	return blk("paragraph", map[string]any{"rich_text": []RichText{{Type: "text", PlainText: s}}})
}

func child(n int, title string) Block {
	b := blk("child_page", map[string]string{"title": title})
	b.ID = id(n)
	return b
}

func mention(n int, text string) Block {
	rt := RichText{Type: "mention", PlainText: text}
	rt.Mention = &struct {
		Type string `json:"type"`
		Page *struct {
			ID string `json:"id"`
		} `json:"page"`
	}{Type: "page", Page: &struct {
		ID string `json:"id"`
	}{ID: id(n)}}
	return blk("paragraph", map[string]any{"rich_text": []RichText{{Type: "text", PlainText: "See "}, rt}})
}

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func newFixture(t *testing.T) (*fakeAPI, *Mirror, *time.Time) {
	t.Helper()
	f := &fakeAPI{hidden: map[string]bool{}, pages: map[string]*fakePage{
		id(0): {title: "NextDesk", edited: t0, blocks: []Block{para("The product. More text."), child(1, "Product"), child(3, "Glossary")}},
		id(1): {title: "Product", edited: t0, blocks: []Block{para("Product docs."), child(2, "Vision")}},
		id(2): {title: "Vision", edited: t0, blocks: []Block{para("Where we go."), mention(3, "Glossary")}},
		id(3): {title: "Glossary", edited: t0, blocks: []Block{para("Words.")}},
	}}
	now := t0.Add(time.Hour)
	m := &Mirror{Dir: filepath.Join(t.TempDir(), "context"), Root: id(0), API: f, Now: func() time.Time { return now }}
	return f, m, &now
}

func refresh(t *testing.T, m *Mirror) Stats {
	t.Helper()
	st, err := m.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() && rel == cacheDir {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMirrorBuild(t *testing.T) {
	_, m, _ := newFixture(t)
	if st := refresh(t, m); st.Added != 4 {
		t.Errorf("stats = %+v", st)
	}
	want := []string{".manifest.json", "INDEX.md", "README.md", "glossary.md", "product/README.md", "product/vision.md"}
	if got := files(t, m.Dir); !slices.Equal(got, want) {
		t.Fatalf("files = %v", got)
	}
	vision := read(t, filepath.Join(m.Dir, "product/vision.md"))
	for _, s := range []string{"title: \"Vision\"", "notion_id: " + id(2), "last_edited: 2026-10-01T09:00:00Z", "See [Glossary](../glossary.md)"} {
		if !strings.Contains(vision, s) {
			t.Errorf("vision.md lacks %q:\n%s", s, vision)
		}
	}
	if root := read(t, filepath.Join(m.Dir, "README.md")); !strings.Contains(root, "[Product](product/README.md)") {
		t.Errorf("root lacks child link:\n%s", root)
	}
	idx := read(t, filepath.Join(m.Dir, IndexFile))
	for _, s := range []string{"- [NextDesk](README.md) — The product.", "  - [Product](product/README.md)", "    - [Vision](product/vision.md) — Where we go."} {
		if !strings.Contains(idx, s) {
			t.Errorf("INDEX.md lacks %q:\n%s", s, idx)
		}
	}
}

func TestMirrorIncremental(t *testing.T) {
	f, m, now := newFixture(t)
	refresh(t, m)
	f.children = nil
	*now = now.Add(15 * time.Minute)
	if st := refresh(t, m); st.Changed() || len(f.children) != 0 {
		t.Fatalf("unchanged refresh: stats %+v, fetched %v", st, f.children)
	}

	// An edit fetches just that page.
	f.pages[id(3)].edited = t0.Add(30 * time.Minute)
	f.pages[id(3)].blocks = []Block{para("New words.")}
	if st := refresh(t, m); st.Updated != 1 || !slices.Equal(f.children, []string{id(3)}) {
		t.Fatalf("edit: stats %+v, fetched %v", st, f.children)
	}
	if g := read(t, filepath.Join(m.Dir, "glossary.md")); !strings.Contains(g, "New words.") {
		t.Errorf("glossary not updated:\n%s", g)
	}
}

func TestMirrorRenameMoveDelete(t *testing.T) {
	f, m, now := newFixture(t)
	refresh(t, m)
	*now = now.Add(time.Hour)
	refresh(t, m)
	f.children = nil

	// Rename: the file moves and the page linking to it follows, without
	// being fetched again.
	f.pages[id(3)].title = "Terms"
	f.pages[id(3)].edited = t0.Add(10 * time.Minute)
	st := refresh(t, m)
	if st.Moved != 1 || slices.Contains(f.children, id(2)) {
		t.Fatalf("rename: stats %+v, fetched %v", st, f.children)
	}
	if v := read(t, filepath.Join(m.Dir, "product/vision.md")); !strings.Contains(v, "See [Terms](../terms.md)") {
		t.Errorf("link not rewritten:\n%s", v)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "glossary.md")); !os.IsNotExist(err) {
		t.Error("old file kept")
	}

	// Move Vision to the root: product/ loses its only child and becomes a file.
	f.pages[id(1)].blocks = []Block{para("Product docs.")}
	f.pages[id(1)].edited = t0.Add(20 * time.Minute)
	f.pages[id(0)].blocks = append(f.pages[id(0)].blocks, child(2, "Vision"))
	f.pages[id(0)].edited = t0.Add(20 * time.Minute)
	refresh(t, m)
	want := []string{".manifest.json", "INDEX.md", "README.md", "product.md", "terms.md", "vision.md"}
	if got := files(t, m.Dir); !slices.Equal(got, want) {
		t.Fatalf("after move: %v", got)
	}
	if v := read(t, filepath.Join(m.Dir, "vision.md")); !strings.Contains(v, "See [Terms](terms.md)") {
		t.Errorf("link not rewritten after move:\n%s", v)
	}

	// Delete Terms: gone from the mirror; links to it point at Notion.
	f.pages[id(3)].trash = true
	f.pages[id(0)].blocks = f.pages[id(0)].blocks[:2]
	f.pages[id(0)].blocks = append(f.pages[id(0)].blocks, child(2, "Vision"))
	f.pages[id(0)].edited = t0.Add(25 * time.Minute)
	if st := refresh(t, m); st.Removed != 1 {
		t.Errorf("delete stats %+v", st)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "terms.md")); !os.IsNotExist(err) {
		t.Error("deleted page kept")
	}
	if v := read(t, filepath.Join(m.Dir, "vision.md")); !strings.Contains(v, "(https://www.notion.so/"+id(3)+")") {
		t.Errorf("link to deleted page:\n%s", v)
	}
	if entries, _ := os.ReadDir(filepath.Join(m.Dir, cacheDir)); len(entries) != 3 {
		t.Errorf("cache not pruned: %d entries", len(entries))
	}
}

func TestMirrorNames(t *testing.T) {
	f := &fakeAPI{hidden: map[string]bool{}, pages: map[string]*fakePage{
		id(0): {title: "Root", edited: t0, blocks: []Block{child(1, "Auth"), child(2, "Auth"), child(3, "🔐 Index"), child(4, "🚀")}},
		id(1): {title: "Auth", edited: t0},
		id(2): {title: "Auth", edited: t0},
		id(3): {title: "🔐 Index", edited: t0},
		id(4): {title: "🚀", edited: t0},
	}}
	m := &Mirror{Dir: filepath.Join(t.TempDir(), "context"), Root: id(0), API: f}
	refresh(t, m)
	want := []string{".manifest.json", "INDEX.md", "README.md", "auth-000b.md", "auth-000c.md", "index-000d.md", "untitled-0000000e.md"}
	if got := files(t, m.Dir); !slices.Equal(got, want) {
		t.Fatalf("files = %v", got)
	}
}

func TestMirrorSearchLag(t *testing.T) {
	f, m, _ := newFixture(t)
	f.hidden[id(2)] = true
	refresh(t, m)
	if _, err := os.Stat(filepath.Join(m.Dir, "product/vision.md")); err != nil {
		t.Fatal("page missing from Search wasn't mirrored")
	}
}

func TestMirrorNoAccess(t *testing.T) {
	f, m, _ := newFixture(t)
	delete(f.pages, id(0))
	if _, err := m.Refresh(context.Background()); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("err = %v, want ErrNoAccess", err)
	}
	if _, err := m.Info(); !errors.Is(err, ErrNoMirror) {
		t.Errorf("info err = %v", err)
	}
}

func TestMirrorRootChange(t *testing.T) {
	_, m, _ := newFixture(t)
	refresh(t, m)
	m.Root = id(1)
	refresh(t, m)
	want := []string{".manifest.json", "INDEX.md", "README.md", "vision.md"}
	if got := files(t, m.Dir); !slices.Equal(got, want) {
		t.Fatalf("files = %v", got)
	}
}

func TestFreeze(t *testing.T) {
	_, m, _ := newFixture(t)
	dst := filepath.Join(t.TempDir(), "frozen")
	if _, err := m.Freeze(dst); !errors.Is(err, ErrNoMirror) {
		t.Fatalf("freeze before refresh: %v", err)
	}
	refresh(t, m)
	os.MkdirAll(dst, 0o700)
	os.WriteFile(filepath.Join(dst, "stale.md"), []byte("x"), 0o600)
	idx, err := m.Freeze(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(idx, "# Index") {
		t.Errorf("index = %q", idx)
	}
	want := []string{"INDEX.md", "README.md", "glossary.md", "product/README.md", "product/vision.md"}
	if got := files(t, dst); !slices.Equal(got, want) {
		t.Fatalf("frozen = %v", got)
	}
}

func TestMirrorSettle(t *testing.T) {
	f, m, now := newFixture(t)
	// Fetched in the same minute as the edit: a later edit in that
	// minute keeps the same last_edited_time, so fetch again next time.
	*now = t0.Add(30 * time.Second)
	refresh(t, m)
	f.children = nil
	*now = t0.Add(time.Hour)
	refresh(t, m)
	if len(f.children) != 4 {
		t.Fatalf("fetched %v, want every page again", f.children)
	}
	f.children = nil
	refresh(t, m)
	if len(f.children) != 0 {
		t.Fatalf("fetched %v after settling", f.children)
	}
}
