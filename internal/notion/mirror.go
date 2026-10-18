package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/galimru/gosha/internal/flock"
	"github.com/galimru/gosha/internal/tmpl"
)

// API is the part of the Notion API the mirror reads.
type API interface {
	Page(ctx context.Context, id string) (*Page, error)
	Children(ctx context.Context, id string) ([]Block, error)
	Search(ctx context.Context) ([]Page, error)
}

const (
	manifestFile = ".manifest.json"
	// IndexFile is the generated map of the tree the agent starts from.
	IndexFile = "INDEX.md"
	// slugMax is the length limit of file and directory names.
	slugMax = 60
	// settle is how soon after an edit a fetch may have missed a later
	// edit: last_edited_time is rounded to the minute, so a page fetched
	// within this window is fetched again on the next refresh.
	settle = 2 * time.Minute
)

// ErrNoMirror means the mirror has never been built.
var ErrNoMirror = errors.New("the project context hasn't been mirrored yet")

// Mirror is a project's Notion page tree as a markdown file tree in Dir.
type Mirror struct {
	Dir string
	// Root is the root page id.
	Root string
	API  API
	// Now is the clock; time.Now if nil.
	Now func() time.Time
}

// entry is one mirrored page in the manifest.
type entry struct {
	Title      string    `json:"title"`
	Icon       string    `json:"icon,omitempty"`
	URL        string    `json:"url"`
	LastEdited time.Time `json:"last_edited"`
	// Fetched is when the page's content was last fetched.
	Fetched time.Time `json:"fetched"`
	// Children are the child pages in Notion's order.
	Children []string `json:"children,omitempty"`
	Path     string   `json:"path"`
}

type manifest struct {
	Root      string            `json:"root"`
	Refreshed time.Time         `json:"refreshed"`
	Pages     map[string]*entry `json:"pages"`
}

// Stats is what a refresh changed.
type Stats struct {
	Added, Updated, Moved, Removed int
}

// Changed reports whether anything changed.
func (s Stats) Changed() bool { return s != Stats{} }

func (s Stats) String() string {
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{s.Added, "added"}, {s.Updated, "updated"}, {s.Moved, "moved"}, {s.Removed, "removed"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
		}
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

func (m *Mirror) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// lock takes the mirror's exclusive lock, so a refresh and a freeze (from
// the pipeline or the CLI) never interleave.
func (m *Mirror) lock() (unlock func(), err error) {
	return flock.Lock(m.Dir + ".lock")
}

func (m *Mirror) load() (*manifest, error) {
	data, err := os.ReadFile(filepath.Join(m.Dir, manifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoMirror
	}
	if err != nil {
		return nil, err
	}
	var man manifest
	if err := json.Unmarshal(data, &man); err != nil {
		return nil, fmt.Errorf("%s: %w", manifestFile, err)
	}
	return &man, nil
}

// Info is a summary of the mirror for status output.
type Info struct {
	Refreshed time.Time
	Pages     int
}

// Info describes the mirror, or returns ErrNoMirror.
func (m *Mirror) Info() (Info, error) {
	man, err := m.load()
	if err != nil {
		return Info{}, err
	}
	return Info{Refreshed: man.Refreshed, Pages: len(man.Pages)}, nil
}

// Refresh brings the mirror up to date. Search tells which pages changed;
// the tree itself always comes from the pages' content (their child page
// blocks), so a page Search doesn't list yet is still found. When only
// page content changed, only those pages are fetched again. When the tree
// changed (a page added, renamed, moved or removed), every page is
// fetched again, so links between pages are resolved from fresh content
// (DESIGN §6.2).
func (m *Mirror) Refresh(ctx context.Context) (Stats, error) {
	unlock, err := m.lock()
	if err != nil {
		return Stats{}, err
	}
	defer unlock()
	old, err := m.load()
	if errors.Is(err, ErrNoMirror) || (err == nil && old.Root != m.Root) {
		old, err = &manifest{Pages: map[string]*entry{}}, nil
	}
	if err != nil {
		return Stats{}, err
	}
	found, err := m.API.Search(ctx)
	if err != nil {
		return Stats{}, err
	}
	search := map[string]*Page{}
	for i := range found {
		search[normID(found[i].ID)] = &found[i]
	}
	w, man, err := m.walk(ctx, old, search, false)
	if err != nil {
		return Stats{}, err
	}
	if w.treeChanged && len(old.Pages) > 0 {
		if w, man, err = m.walk(ctx, old, search, true); err != nil {
			return Stats{}, err
		}
	}
	if err := m.write(man, w.bodies); err != nil {
		return Stats{}, err
	}
	return w.stats, nil
}

// walk visits the tree from the root and lays it out. With all set, every
// page is fetched; otherwise unchanged pages keep their mirrored content.
func (m *Mirror) walk(ctx context.Context, old *manifest, search map[string]*Page, all bool) (*walk, *manifest, error) {
	w := &walk{m: m, ctx: ctx, old: old, search: search, all: all, pages: map[string]*entry{}, bodies: map[string]string{}}
	if err := w.visit(m.Root, true); err != nil {
		return nil, nil, err
	}
	man := &manifest{Root: m.Root, Refreshed: m.now().UTC(), Pages: w.pages}
	layout(man)
	for id, e := range man.Pages {
		prev := old.Pages[id]
		switch {
		case prev == nil:
			w.treeChanged = true
		case prev.Path != e.Path:
			w.stats.Moved++
			w.treeChanged = true
		case prev.Title != e.Title:
			w.treeChanged = true
		}
	}
	for id := range old.Pages {
		if man.Pages[id] == nil {
			w.stats.Removed++
			w.treeChanged = true
		}
	}
	return w, man, nil
}

type walk struct {
	m      *Mirror
	ctx    context.Context
	old    *manifest
	search map[string]*Page
	// all fetches every page, changed or not.
	all   bool
	pages map[string]*entry
	// bodies are the pages' rendered content: links to other pages are
	// placeholders in fetched pages and already resolved in kept ones.
	bodies      map[string]string
	stats       Stats
	treeChanged bool
}

func (w *walk) visit(id string, root bool) error {
	if _, seen := w.pages[id]; seen {
		return nil
	}
	prev := w.old.Pages[id]
	page := w.search[id]
	if page == nil {
		// Not in Search (yet): ask for the page itself.
		p, err := w.m.API.Page(w.ctx, id)
		if errors.Is(err, ErrNoAccess) && !root {
			return nil
		}
		if err != nil {
			return fmt.Errorf("page %s: %w", id, err)
		}
		page = p
	}
	if page.Gone() {
		if root {
			return fmt.Errorf("the root page is in the trash: %w", ErrNoAccess)
		}
		return nil
	}
	e := &entry{Title: page.Title(), Icon: page.Emoji(), URL: page.URL, LastEdited: page.LastEditedTime.UTC()}
	var kept string
	keep := !w.all && prev != nil && prev.LastEdited.Equal(e.LastEdited) && prev.Fetched.Sub(prev.LastEdited) >= settle
	if keep {
		var err error
		kept, err = w.m.readBody(prev.Path)
		keep = err == nil
	}
	if !keep {
		blocks, err := w.m.blocks(w.ctx, id)
		if errors.Is(err, ErrNoAccess) && !root {
			return nil
		}
		if err != nil {
			return fmt.Errorf("page %s: %w", id, err)
		}
		r := render(blocks)
		w.bodies[id] = r.Body
		e.Fetched, e.Children = w.m.now().UTC(), r.Children
		if prev == nil {
			w.stats.Added++
		} else if !prev.LastEdited.Equal(e.LastEdited) || prev.Title != e.Title {
			w.stats.Updated++
		}
	} else {
		w.bodies[id] = kept
		e.Fetched, e.Children = prev.Fetched, slices.Clone(prev.Children)
		if prev.Title != e.Title || prev.Icon != e.Icon {
			w.stats.Updated++
		}
	}
	w.pages[id] = e
	for _, c := range e.Children {
		if err := w.visit(c, false); err != nil {
			return err
		}
	}
	// Children that turned out to be gone aren't part of the tree.
	e.Children = slices.DeleteFunc(e.Children, func(c string) bool { return w.pages[c] == nil })
	return nil
}

// blocks fetches a page's blocks with their nested children. Child pages
// and databases are separate pages, so their content isn't descended into.
func (m *Mirror) blocks(ctx context.Context, id string) ([]Block, error) {
	bs, err := m.API.Children(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range bs {
		b := &bs[i]
		if b.HasChildren && b.Type != "child_page" && b.Type != "child_database" {
			if b.Children, err = m.blocks(ctx, b.ID); err != nil {
				return nil, err
			}
		}
	}
	return bs, nil
}

// layout assigns every page its path (DESIGN §6.1): the root is
// README.md, a page with child pages is a directory with its own content
// in README.md, any other page is <slug>.md.
func layout(man *manifest) {
	root := man.Pages[man.Root]
	root.Path = "README.md"
	var place func(e *entry, dir string)
	place = func(e *entry, dir string) {
		names := map[string]int{}
		base := map[string]string{}
		for _, c := range e.Children {
			b := tmpl.Slug(man.Pages[c].Title, slugMax)
			if b == "" {
				b = "untitled-" + c[len(c)-8:]
			}
			base[c] = b
			names[b]++
		}
		for _, c := range e.Children {
			ce := man.Pages[c]
			b := base[c]
			// Duplicates, and names that clash with README.md or
			// INDEX.md (on case-insensitive file systems too), get an
			// id suffix.
			if names[b] > 1 || b == "readme" || (dir == "" && b == "index") {
				b += "-" + c[len(c)-4:]
			}
			if len(ce.Children) > 0 {
				ce.Path = path.Join(dir, b, "README.md")
				place(ce, path.Join(dir, b))
			} else {
				ce.Path = path.Join(dir, b+".md")
			}
		}
	}
	place(root, "")
}

// readBody is a mirrored page's content without its front-matter.
func (m *Mirror) readBody(rel string) (string, error) {
	data, err := os.ReadFile(filepath.Join(m.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	s, ok := strings.CutPrefix(string(data), "---\n")
	if ok {
		_, s, ok = strings.Cut(s, "\n---\n")
	}
	if !ok {
		return "", fmt.Errorf("%s: no front-matter", rel)
	}
	return strings.TrimPrefix(s, "\n"), nil
}

// write renders every page and INDEX.md into Dir, writing only files that
// changed and removing files that are no longer part of the tree. The
// manifest is written last.
func (m *Mirror) write(man *manifest, bodies map[string]string) error {
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	want := map[string][]byte{}
	for id, e := range man.Pages {
		body := bodies[id]
		from := path.Dir(e.Path)
		resolved := resolve(body, func(t string) (string, string, bool) {
			te := man.Pages[t]
			if te == nil {
				return "", "", false
			}
			rel, err := filepath.Rel(from, te.Path)
			if err != nil {
				return "", "", false
			}
			return filepath.ToSlash(rel), te.Title, true
		})
		want[e.Path] = []byte(frontMatter(id, e) + "\n" + resolved)
	}
	want[IndexFile] = []byte(index(man, bodies))
	have := map[string]bool{}
	err := filepath.WalkDir(m.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(m.Dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() || rel == manifestFile {
			return nil
		}
		have[rel] = true
		return nil
	})
	if err != nil {
		return err
	}
	for rel := range have {
		if want[rel] == nil {
			if err := os.Remove(filepath.Join(m.Dir, filepath.FromSlash(rel))); err != nil {
				return err
			}
		}
	}
	for rel, data := range want {
		p := filepath.Join(m.Dir, filepath.FromSlash(rel))
		if have[rel] {
			if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, data) {
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return err
		}
	}
	if err := pruneDirs(m.Dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(m.Dir, manifestFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.Dir, manifestFile))
}

// pruneDirs removes empty directories under dir.
func pruneDirs(dir string) error {
	var dirs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != dir {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Deepest first, so a parent left empty goes too.
	slices.Reverse(dirs)
	for _, d := range dirs {
		if entries, err := os.ReadDir(d); err == nil && len(entries) == 0 {
			if err := os.Remove(d); err != nil {
				return err
			}
		}
	}
	return nil
}

// quoteYAML quotes s as a YAML double-quoted scalar (JSON's syntax is a
// subset of it).
func quoteYAML(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func frontMatter(id string, e *entry) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: " + quoteYAML(e.Title) + "\n")
	if e.Icon != "" {
		b.WriteString("icon: " + quoteYAML(e.Icon) + "\n")
	}
	b.WriteString("notion_id: " + id + "\n")
	b.WriteString("url: " + e.URL + "\n")
	b.WriteString("last_edited: " + e.LastEdited.Format(time.RFC3339) + "\n")
	b.WriteString("---\n")
	return b.String()
}

// index is INDEX.md: the whole tree in Notion's order, with each page's
// path, first sentence and last edit.
func index(man *manifest, bodies map[string]string) string {
	var b strings.Builder
	b.WriteString("# Index\n\nThe project's Notion pages, mirrored read-only. Each line is a page: title, file (relative to this index), first sentence, last edit.\n\n")
	var walk func(id, indent string)
	walk = func(id, indent string) {
		e := man.Pages[id]
		title := e.Title
		if title == "" {
			title = "Untitled"
		}
		line := indent + "- [" + escapeLinkText(title) + "](" + e.Path + ")"
		if s := sentenceOf(bodies[id]); s != "" {
			line += " — " + s
		}
		line += " _(edited " + e.LastEdited.Format("2006-01-02") + ")_"
		b.WriteString(line + "\n")
		for _, c := range e.Children {
			walk(c, indent+"  ")
		}
	}
	walk(man.Root, "")
	return b.String()
}

var (
	mdLinkRe  = regexp.MustCompile(`\[((?:\\.|[^\]\\])*)\]\([^)]*\)`)
	sentEndRe = regexp.MustCompile(`[.!?](\s|$)`)
)

// sentenceOf is the first sentence of a page body's first paragraph, as
// plain text.
func sentenceOf(body string) string {
	inCode := false
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") {
			inCode = !inCode
			continue
		}
		if inCode || t == "" || strings.ContainsAny(t[:1], "#<|>-$[*") || strings.HasPrefix(t, "1. ") {
			continue
		}
		t = mdLinkRe.ReplaceAllString(t, "$1")
		t = strings.NewReplacer("**", "", "~~", "", `\[`, "[", `\]`, "]").Replace(t)
		if loc := sentEndRe.FindStringIndex(t); loc != nil {
			t = t[:loc[0]+1]
		}
		if r := []rune(t); len(r) > 200 {
			t = string(r[:200]) + "…"
		}
		return t
	}
	return ""
}

// Freeze copies the mirror (without Gosha's bookkeeping) to dst, replacing
// whatever is there, and returns INDEX.md. A run reads only its copy, so
// a refresh during the run can't change what the agent sees.
func (m *Mirror) Freeze(dst string) (string, error) {
	unlock, err := m.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err := m.load(); err != nil {
		return "", err
	}
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	err = filepath.WalkDir(m.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(m.Dir, p)
		if rel == manifestFile || strings.HasPrefix(rel, manifestFile) {
			return nil
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o700)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o600)
	})
	if err != nil {
		return "", err
	}
	idx, err := os.ReadFile(filepath.Join(dst, IndexFile))
	return string(idx), err
}

// Index returns the mirror's INDEX.md.
func (m *Mirror) Index() (string, error) {
	if _, err := m.load(); err != nil {
		return "", err
	}
	idx, err := os.ReadFile(filepath.Join(m.Dir, IndexFile))
	return string(idx), err
}
