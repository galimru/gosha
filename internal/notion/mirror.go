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
	"syscall"
	"time"

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
	cacheDir     = ".cache"
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
	if err := os.MkdirAll(filepath.Dir(m.Dir), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(m.Dir+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
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
// blocks), so a page Search doesn't list yet is still found. Only pages
// that changed are fetched again.
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
	w := &walk{m: m, ctx: ctx, old: old, search: search, pages: map[string]*entry{}}
	if err := os.MkdirAll(filepath.Join(m.Dir, cacheDir), 0o700); err != nil {
		return Stats{}, err
	}
	if err := w.visit(m.Root, true); err != nil {
		return Stats{}, err
	}
	man := &manifest{Root: m.Root, Refreshed: m.now().UTC(), Pages: w.pages}
	layout(man)
	for id, e := range man.Pages {
		if prev := old.Pages[id]; prev != nil && prev.Path != e.Path {
			w.stats.Moved++
		}
	}
	for id := range old.Pages {
		if man.Pages[id] == nil {
			w.stats.Removed++
		}
	}
	if err := m.write(man); err != nil {
		return Stats{}, err
	}
	return w.stats, nil
}

type walk struct {
	m      *Mirror
	ctx    context.Context
	old    *manifest
	search map[string]*Page
	pages  map[string]*entry
	stats  Stats
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
	cache := filepath.Join(w.m.Dir, cacheDir, id+".md")
	_, cerr := os.Stat(cache)
	if prev == nil || !prev.LastEdited.Equal(e.LastEdited) || prev.Fetched.Sub(prev.LastEdited) < settle || cerr != nil {
		blocks, err := w.m.blocks(w.ctx, id)
		if errors.Is(err, ErrNoAccess) && !root {
			return nil
		}
		if err != nil {
			return fmt.Errorf("page %s: %w", id, err)
		}
		r := render(blocks)
		if err := os.WriteFile(cache, []byte(r.Body), 0o600); err != nil {
			return err
		}
		e.Fetched, e.Children = w.m.now().UTC(), r.Children
		if prev == nil {
			w.stats.Added++
		} else if !prev.LastEdited.Equal(e.LastEdited) || prev.Title != e.Title {
			w.stats.Updated++
		}
	} else {
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

// write renders every page and INDEX.md into Dir, writing only files that
// changed and removing files that are no longer part of the tree. The
// manifest is written last.
func (m *Mirror) write(man *manifest) error {
	want := map[string][]byte{}
	bodies := map[string]string{}
	for id, e := range man.Pages {
		body, err := os.ReadFile(filepath.Join(m.Dir, cacheDir, id+".md"))
		if err != nil {
			return err
		}
		bodies[id] = string(body)
		from := path.Dir(e.Path)
		resolved := resolve(string(body), func(t string) (string, string, bool) {
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
		switch {
		case rel == cacheDir && d.IsDir():
			return filepath.SkipDir
		case d.IsDir() || rel == manifestFile:
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
	if err := m.pruneCache(man); err != nil {
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

func (m *Mirror) pruneCache(man *manifest) error {
	entries, err := os.ReadDir(filepath.Join(m.Dir, cacheDir))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if man.Pages[strings.TrimSuffix(e.Name(), ".md")] == nil {
			if err := os.Remove(filepath.Join(m.Dir, cacheDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
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
		if rel == cacheDir || rel == manifestFile || strings.HasPrefix(rel, manifestFile) {
			if d.IsDir() {
				return filepath.SkipDir
			}
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
