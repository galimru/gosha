package tmpl

import "testing"

func TestSlug(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"Add login", 40, "add-login"},
		{"🔐 Authentication & Authorization", 60, "authentication-authorization"},
		{"  --Fix: crash on  /restart!! ", 40, "fix-crash-on-restart"},
		{"Ünïcode títle", 40, "n-code-t-tle"},
		{"", 40, ""},
		{"a very long title that keeps going well past the limit", 20, "a-very-long-title-th"},
		{"cut at a dash boundary here", 6, "cut-at"},
		{"cut at- dash", 7, "cut-at"},
	}
	for _, c := range cases {
		if got := Slug(c.in, c.max); got != c.want {
			t.Errorf("Slug(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestRenderFuncs(t *testing.T) {
	d := Data{Ticket: Ticket{ID: "NXT-12", Title: "Add login"}}
	cases := map[string]string{
		"gosha/{{.Ticket.ID}}-{{.Ticket.Title | slug}}":   "gosha/NXT-12-add-login",
		"{{.Ticket.ID | lower}}-{{.Ticket.Title | slug}}": "nxt-12-add-login",
		"{{.Ticket.Title | trunc 3 | upper}}":             "ADD",
	}
	for pattern, want := range cases {
		got, err := Render("branch", pattern, d)
		if err != nil {
			t.Fatalf("%s: %v", pattern, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", pattern, got, want)
		}
	}
}

func TestRenderUnknownField(t *testing.T) {
	if _, err := Render("branch", "{{.Ticket.Nope}}", Data{}); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}
