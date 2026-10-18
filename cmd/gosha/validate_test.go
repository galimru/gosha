package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRepo(t *testing.T) {
	dir := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		path = filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateRepo(dir); err == nil {
		t.Error("missing config: expected an error")
	}

	cfg := "name: demo\nlinear:\n  team: NXT\nnotion:\n  root_page: 1a2b3c4d5e6f708192a3b4c5d6e7f809\n"
	write(".gosha/config.yaml", cfg)
	if err := validateRepo(dir); err != nil {
		t.Errorf("valid config: %v", err)
	}

	write(".gosha/config.yaml", cfg+"deploy:\n  workflow: deploy.yml\n")
	if err := validateRepo(dir); err == nil || !strings.Contains(err.Error(), "deploy.yml") {
		t.Errorf("missing workflow: %v", err)
	}
	write(".github/workflows/deploy.yml", "on: push\n")
	if err := validateRepo(dir); err != nil {
		t.Errorf("with workflow: %v", err)
	}

	write(".gosha/config.yaml", "name: demo\n")
	if err := validateRepo(dir); err == nil {
		t.Error("invalid config: expected an error")
	}
}
