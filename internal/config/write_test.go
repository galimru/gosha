package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.env")
	// A new file starts from the template.
	if err := SetSecret(path, KeyNotionToken, "ntn_1"); err != nil {
		t.Fatal(err)
	}
	m, err := ReadEnvFile(path)
	if err != nil || m[KeyNotionToken] != "ntn_1" || len(m) != 5 {
		t.Fatalf("secrets = %v, %v", m, err)
	}

	os.WriteFile(path, []byte("# mine\nexport NOTION_TOKEN=old\nOTHER=x # kept\nLINEAR_CLIENT_ID=id"), 0o644)
	if err := SetSecret(path, KeyNotionToken, "ntn_2"); err != nil {
		t.Fatal(err)
	}
	if err := SetSecret(path, KeyClaudeToken, "sk"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := "# mine\nNOTION_TOKEN=ntn_2\nOTHER=x # kept\nLINEAR_CLIENT_ID=id\nCLAUDE_CODE_OAUTH_TOKEN=sk\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode())
	}
	if err := SetSecret(path, KeyNotionToken, "a\nb=c"); err == nil {
		t.Error("newline in a value accepted")
	}
}

func TestSetGlobal(t *testing.T) {
	h := Home(t.TempDir())
	path := h.Config()
	// A new file is the starter, which parses once both fields are set.
	if err := SetGlobal(path, "github_app.app_id", "123456"); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobal(path, "telegram.chat_id", "-1001234567890"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(h.Path("github-app.pem"), []byte("key"), 0o600)
	g, err := LoadGlobal(h)
	if err != nil {
		t.Fatal(err)
	}
	if g.GitHubApp.AppID != 123456 || g.Telegram.ChatID != -1001234567890 {
		t.Errorf("global = %+v", g)
	}
	data, _ := os.ReadFile(path)
	if want := strings.Replace(strings.Replace(StarterGlobal, "app_id: 0", "app_id: 123456", 1),
		"chat_id: 0", "chat_id: -1001234567890", 1); string(data) != want {
		t.Errorf("file =\n%s", data)
	}
	if v, err := GlobalValue(path, "telegram.chat_id"); err != nil || v != "-1001234567890" {
		t.Errorf("GlobalValue = %q, %v", v, err)
	}
	if v, err := GlobalValue(filepath.Join(t.TempDir(), "none.yaml"), "telegram.chat_id"); err != nil || v != "" {
		t.Errorf("GlobalValue of a missing file = %q, %v", v, err)
	}
}

func TestSetGlobalKeepsLayout(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"comment column kept",
			"concurrency: 2\n\ngithub_app:\n  app_id: 1         # mine\n\ntelegram: {chat_id: 5}\n",
			"concurrency: 2\n\ngithub_app:\n  app_id: 123456    # mine\n\ntelegram: {chat_id: 5}\n"},
		{"key added to a section",
			"github_app:\n    private_key_file: k.pem\n",
			"github_app:\n    app_id: 123456\n    private_key_file: k.pem\n"},
		{"empty section",
			"# top\ngithub_app:\nretention: 7d\n",
			"# top\ngithub_app:\n  app_id: 123456\nretention: 7d\n"},
		{"section added",
			"concurrency: 1",
			"concurrency: 1\n\ngithub_app:\n  app_id: 123456\n"},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		os.WriteFile(path, []byte(c.in), 0o600)
		if err := SetGlobal(path, "github_app.app_id", "123456"); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if data, _ := os.ReadFile(path); string(data) != c.want {
			t.Errorf("%s: file = %q, want %q", c.name, data, c.want)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode changed to %v", c.name, fi.Mode())
		}
	}
	// A flow mapping can't be edited in place: the error says what to set.
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("telegram: {chat_id: 5}\n"), 0o600)
	if err := SetGlobal(path, "telegram.chat_id", "7"); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Errorf("flow mapping: %v", err)
	}
}
