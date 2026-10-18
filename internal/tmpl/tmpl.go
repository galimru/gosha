// Package tmpl holds the template data model and functions shared by every
// Gosha template: prompts and the branch name pattern (DESIGN §5.3).
package tmpl

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"unicode"
)

// Data is the one data model every template renders against.
type Data struct {
	Project    string
	Ticket     Ticket
	Context    Context
	Branch     string
	BaseBranch string
	Diff       string
	Feedback   Feedback
	Round      int
	ResultFile string
}

type Ticket struct {
	ID          string
	Team        string
	Number      int
	Title       string
	Description string
	URL         string
	Comments    []Comment
}

type Comment struct {
	Author string
	Body   string
}

type Context struct {
	Dir   string
	Index string
}

type Feedback struct {
	// Review is the reviewer's comments and human PR feedback (address).
	Review string
	// CI is the failed-check logs (address, M4).
	CI string
	// Previous is the last round's review and the implementer's reply
	// (review, from round 2).
	Previous string
}

// SlugMax is the length limit of the template `slug` function.
const SlugMax = 40

// Funcs is the function map available in every template.
var Funcs = template.FuncMap{
	"slug":  func(s string) string { return Slug(s, SlugMax) },
	"lower": strings.ToLower,
	"upper": strings.ToUpper,
	"trunc": trunc,
}

// Slug lowercases s, turns every run of non-alphanumerics into a single '-',
// trims dashes from both ends and cuts the result to at most max characters.
func Slug(s string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > max {
		out = strings.TrimRight(out[:max], "-")
	}
	return out
}

// trunc is used as `{{.Ticket.Title | trunc 20}}`, so the length comes first.
func trunc(n int, s string) string {
	r := []rune(s)
	if n < 0 || len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Parse parses a named template with Funcs and strict missing-key handling.
func Parse(name, text string) (*template.Template, error) {
	t, err := template.New(name).Funcs(Funcs).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	return t, nil
}

// Render parses and executes text against d.
func Render(name, text string, d Data) (string, error) {
	t, err := Parse(name, text)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return "", fmt.Errorf("template %s: %w", name, err)
	}
	return buf.String(), nil
}
