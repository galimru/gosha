package notion

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Links to pages are rendered as placeholders, `[text](notion-page:<id>)`,
// and resolved to paths inside the mirror when files are written, so a
// renamed or moved page doesn't need every page linking to it re-fetched.
// A `#title` suffix means the link text is the target's current title.
const pageScheme = "notion-page:"

// pageLink is the placeholder for a link to page id; withTitle takes the
// target's current title as the text.
func pageLink(text, id string, withTitle bool) string {
	l := "[" + escapeLinkText(text) + "](" + pageScheme + normID(id)
	if withTitle {
		l += "#title"
	}
	return l + ")"
}

func escapeLinkText(s string) string {
	return strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`).Replace(s)
}

// rendered is a page body in markdown with its child pages in order.
type rendered struct {
	Body     string
	Children []string
}

// render turns a page's blocks into markdown.
func render(blocks []Block) rendered {
	r := &renderer{}
	body := r.blocks(blocks, "")
	return rendered{Body: strings.TrimSpace(body) + "\n", Children: r.children}
}

type renderer struct {
	children []string
}

// listKind groups consecutive blocks that form one tight list.
func listKind(t string) string {
	switch t {
	case "bulleted_list_item", "numbered_list_item", "to_do":
		return t
	}
	return ""
}

func (r *renderer) blocks(bs []Block, indent string) string {
	var b strings.Builder
	prev := ""
	for i, bl := range bs {
		out := r.block(bl, indent)
		if out == "" {
			continue
		}
		kind := listKind(bl.Type)
		if i > 0 && b.Len() > 0 {
			if kind != "" && kind == prev {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
			}
		}
		b.WriteString(out)
		prev = kind
	}
	return b.String()
}

// text is the rich_text payload most block types share.
type text struct {
	RichText []RichText `json:"rich_text"`
	Checked  bool       `json:"checked"`
	Language string     `json:"language"`
	Caption  []RichText `json:"caption"`
	Icon     *Icon      `json:"icon"`
}

func (r *renderer) block(bl Block, indent string) string {
	var d text
	_ = json.Unmarshal(bl.Data, &d)
	line := func(prefix string) string { return indent + prefix + inline(d.RichText) }
	// nested renders children under a list item, indented.
	nested := func(s, by string) string {
		if len(bl.Children) == 0 {
			return s
		}
		return s + "\n" + r.blocks(bl.Children, indent+by)
	}
	// after renders children as their own blocks at the same level.
	after := func(s string) string {
		if len(bl.Children) == 0 {
			return s
		}
		return s + "\n\n" + r.blocks(bl.Children, indent)
	}
	switch bl.Type {
	case "paragraph":
		return after(line(""))
	case "heading_1", "heading_2", "heading_3":
		return after(line(strings.Repeat("#", int(bl.Type[len(bl.Type)-1]-'0')) + " "))
	case "bulleted_list_item":
		return nested(line("- "), "  ")
	case "numbered_list_item":
		return nested(line("1. "), "   ")
	case "to_do":
		box := "[ ] "
		if d.Checked {
			box = "[x] "
		}
		return nested(line("- "+box), "  ")
	case "toggle":
		return after(line(""))
	case "quote":
		return quote(inline(d.RichText), indent, r, bl, "")
	case "callout":
		icon := ""
		if d.Icon != nil && d.Icon.Type == "emoji" {
			icon = d.Icon.Emoji + " "
		}
		return quote(inline(d.RichText), indent, r, bl, icon)
	case "code":
		s := indentLines("```"+d.Language+"\n"+plain(d.RichText)+"\n```", indent)
		if c := inline(d.Caption); c != "" {
			s += "\n\n" + indent + c
		}
		return s
	case "equation":
		var e struct {
			Expression string `json:"expression"`
		}
		_ = json.Unmarshal(bl.Data, &e)
		return indent + "$$\n" + e.Expression + "\n$$"
	case "divider":
		return indent + "---"
	case "table":
		return r.table(bl, indent)
	case "bookmark", "embed", "link_preview":
		var l struct {
			URL     string     `json:"url"`
			Caption []RichText `json:"caption"`
		}
		_ = json.Unmarshal(bl.Data, &l)
		t := inline(l.Caption)
		if t == "" {
			t = l.URL
		}
		return indent + linkTo(t, l.URL)
	case "image", "file", "pdf", "video", "audio":
		return indent + media(bl)
	case "child_page":
		var c struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(bl.Data, &c)
		r.children = append(r.children, normID(bl.ID))
		return indent + pageLink(c.Title, bl.ID, true)
	case "child_database":
		var c struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(bl.Data, &c)
		return indent + fmt.Sprintf("<!-- database %q not mirrored -->", c.Title)
	case "link_to_page":
		var l struct {
			Type   string `json:"type"`
			PageID string `json:"page_id"`
		}
		_ = json.Unmarshal(bl.Data, &l)
		if l.Type != "page_id" {
			return indent + "<!-- link to a database not mirrored -->"
		}
		return indent + pageLink("", l.PageID, true)
	case "synced_block", "column_list", "column":
		return r.blocks(bl.Children, indent)
	case "table_of_contents", "breadcrumb":
		return ""
	}
	return indent + "<!-- unsupported: " + bl.Type + " -->"
}

func indentLines(s, indent string) string {
	if indent == "" {
		return s
	}
	return indent + strings.ReplaceAll(s, "\n", "\n"+indent)
}

// quote renders a quote or callout: its text and children, all prefixed.
func quote(head, indent string, r *renderer, bl Block, icon string) string {
	s := icon + head
	if len(bl.Children) > 0 {
		s += "\n\n" + r.blocks(bl.Children, "")
	}
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, strings.TrimRight(indent+"> "+l, " "))
	}
	return strings.Join(out, "\n")
}

func (r *renderer) table(bl Block, indent string) string {
	var t struct {
		HasColumnHeader bool `json:"has_column_header"`
	}
	_ = json.Unmarshal(bl.Data, &t)
	var rows [][]string
	width := 0
	for _, row := range bl.Children {
		var c struct {
			Cells [][]RichText `json:"cells"`
		}
		_ = json.Unmarshal(row.Data, &c)
		var cells []string
		for _, cell := range c.Cells {
			cells = append(cells, strings.ReplaceAll(strings.ReplaceAll(inline(cell), "|", `\|`), "\n", "<br>"))
		}
		width = max(width, len(cells))
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return ""
	}
	if !t.HasColumnHeader {
		rows = append([][]string{make([]string, width)}, rows...)
	}
	var b strings.Builder
	for i, row := range rows {
		for len(row) < width {
			row = append(row, "")
		}
		b.WriteString(indent + "| " + strings.Join(row, " | ") + " |")
		if i == 0 {
			b.WriteString("\n" + indent + strings.Repeat("| --- ", width) + "|")
		}
		if i < len(rows)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// media renders an image or file as a link with its caption. Files hosted
// by Notion have expiring URLs, so only their caption or name is kept.
func media(bl Block) string {
	var m struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		External *struct {
			URL string `json:"url"`
		} `json:"external"`
		Caption []RichText `json:"caption"`
	}
	_ = json.Unmarshal(bl.Data, &m)
	label := inline(m.Caption)
	if label == "" {
		label = m.Name
	}
	if m.External != nil {
		if label == "" {
			label = m.External.URL
		}
		return bl.Type + ": " + linkTo(label, m.External.URL)
	}
	if label == "" {
		return "<!-- " + bl.Type + " hosted in Notion, not mirrored -->"
	}
	return bl.Type + ": " + label + " <!-- hosted in Notion, not mirrored -->"
}

// linkTo is a markdown link, or a page placeholder for links into Notion.
func linkTo(text, href string) string {
	if id, ok := notionPage(href); ok {
		return pageLink(text, id, false)
	}
	return "[" + escapeLinkText(text) + "](" + href + ")"
}

// notionPage reports whether href points at a Notion page: a notion.so or
// notion.site URL, or a workspace-relative "/<id>" link.
func notionPage(href string) (string, bool) {
	u, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(u.Host)
	if host != "" && !strings.HasSuffix(host, "notion.so") && !strings.HasSuffix(host, "notion.site") {
		return "", false
	}
	if host == "" && !strings.HasPrefix(href, "/") {
		return "", false
	}
	id, err := ParsePageID(u.Path)
	return id, err == nil
}

// plain is rich text without formatting.
func plain(rt []RichText) string {
	var b strings.Builder
	for _, t := range rt {
		b.WriteString(t.PlainText)
	}
	return b.String()
}

// inline is rich text as markdown.
func inline(rt []RichText) string {
	var b strings.Builder
	for _, t := range rt {
		s := t.PlainText
		switch {
		case t.Type == "equation":
			b.WriteString("$" + s + "$")
			continue
		case t.Mention != nil && t.Mention.Type == "page" && t.Mention.Page != nil:
			b.WriteString(pageLink(s, t.Mention.Page.ID, true))
			continue
		}
		a := t.Annotations
		if a.Code {
			s = "`" + s + "`"
		}
		if t.Href != "" {
			s = linkTo(s, t.Href)
		}
		if a.Bold {
			s = wrap(s, "**")
		}
		if a.Italic {
			s = wrap(s, "_")
		}
		if a.Strikethrough {
			s = wrap(s, "~~")
		}
		b.WriteString(s)
	}
	return b.String()
}

// wrap puts markers around s's text, keeping surrounding spaces outside,
// where markdown needs them.
func wrap(s, m string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return s
	}
	i := strings.Index(s, t)
	return s[:i] + m + t + m + s[i+len(t):]
}

var placeholderRe = regexp.MustCompile(`\[((?:\\.|[^\]\\])*)\]\(` + pageScheme + `([0-9a-f]{32})(#title)?\)`)

// resolve rewrites the page placeholders in body. target returns the
// link and current title of a page id, ok=false for pages outside the
// mirror, which link to Notion instead.
func resolve(body string, target func(id string) (link, title string, ok bool)) string {
	return placeholderRe.ReplaceAllStringFunc(body, func(m string) string {
		sm := placeholderRe.FindStringSubmatch(m)
		text, id, withTitle := sm[1], sm[2], sm[3] != ""
		link, title, ok := target(id)
		if !ok {
			link = "https://www.notion.so/" + id
		} else if withTitle {
			text = escapeLinkText(title)
		}
		if text == "" {
			text = escapeLinkText(link)
		}
		return "[" + text + "](" + link + ")"
	})
}
