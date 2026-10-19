package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/galimru/gosha/internal/config"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runCLIIn(t, "", args...)
}

func runCLIIn(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	a := &app{}
	root := a.rootCmd()
	var out bytes.Buffer
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--home", t.TempDir()}, args...))
	err := root.Execute()
	return out.String(), err
}

const page = "1a2b3c4d5e6f708192a3b4c5d6e7f809"

func TestInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Next Desk")
	os.MkdirAll(dir, 0o755)
	out, err := runCLIIn(t, "\nnxt\n"+page+"\n", "init", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Project name [next-desk]: ") {
		t.Errorf("prompt = %q", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, config.ProjectFile))
	if err != nil {
		t.Fatal(err)
	}
	p, err := config.ParseProject(data, "")
	if err != nil || p.Name != "next-desk" || p.Linear.Team != "NXT" {
		t.Fatalf("written config = %+v, %v", p, err)
	}
	// An existing config fails before any question.
	if out, err := runCLIIn(t, "", "init", dir); err == nil || !strings.Contains(err.Error(), "already exists") || strings.Contains(out, "Project name") {
		t.Errorf("overwrite: %v, %q", err, out)
	}
}

func TestInitAsksAgain(t *testing.T) {
	dir := t.TempDir()
	out, err := runCLIIn(t, "Bad Name\ndesk\n\nNXT\nnot a page\n"+page+"\n", "init", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"must be lowercase", "required", "must be a Notion page URL or id"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, config.ProjectFile))
	if p, err := config.ParseProject(data, ""); err != nil || p.Name != "desk" {
		t.Fatalf("written config = %+v, %v", p, err)
	}
}

func TestInitNeedsInput(t *testing.T) {
	dir := t.TempDir()
	for _, in := range []string{"", "desk\nNXT\n"} {
		if _, err := runCLIIn(t, in, "init", dir); err == nil || !strings.Contains(err.Error(), "answers on stdin") {
			t.Errorf("stdin %q: %v", in, err)
		}
		if _, err := os.Stat(filepath.Join(dir, config.ProjectFile)); !os.IsNotExist(err) {
			t.Errorf("stdin %q: config written", in)
		}
	}
}
