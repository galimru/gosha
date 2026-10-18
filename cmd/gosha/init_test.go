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
	a := &app{}
	root := a.rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--home", t.TempDir()}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Next Desk")
	os.MkdirAll(dir, 0o755)
	if _, err := runCLI(t, "init", dir); err == nil || !strings.Contains(err.Error(), "--team and --notion required") {
		t.Fatalf("missing flags: %v", err)
	}
	if _, err := runCLI(t, "init", dir, "--team", "NXT", "--notion", "1a2b3c4d5e6f708192a3b4c5d6e7f809"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, config.ProjectFile))
	if err != nil {
		t.Fatal(err)
	}
	p, err := config.ParseProject(data, "")
	if err != nil || p.Name != "next-desk" || p.Linear.Team != "NXT" {
		t.Fatalf("written config = %+v, %v", p, err)
	}
	if _, err := runCLI(t, "init", dir, "--team", "NXT", "--notion", "x"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("overwrite: %v", err)
	}
}

func TestInitRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	if _, err := runCLI(t, "init", dir, "--team", "NXT", "--notion", "not a page"); err == nil || !strings.Contains(err.Error(), "notion.root_page") {
		t.Fatalf("invalid page: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.ProjectFile)); !os.IsNotExist(err) {
		t.Error("invalid config left behind")
	}
}
