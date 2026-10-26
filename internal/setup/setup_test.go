package setup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/prompt"
)

// fakes serves Linear, Notion, Telegram and GitHub.
type fakes struct {
	t   *testing.T
	srv *httptest.Server
	pem []byte

	mu          sync.Mutex
	permissions string // the App's permissions JSON
	updates     [][]string
	sent        []string // "chatID: text"
	conversions int
}

func newFakes(t *testing.T) *fakes {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakes{t: t, pem: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		permissions: `{"contents":"write","pull_requests":"write","checks":"read","actions":"read","statuses":"read","metadata":"read"}`}
	mux := http.NewServeMux()
	mux.HandleFunc("/linear/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_id") != "lin-id" || r.Form.Get("client_secret") != "lin-secret" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"access_token":"lt","expires_in":3600}`)
	})
	mux.HandleFunc("/linear/graphql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"viewer":{"id":"u","name":"Gosha","organization":{"name":"Acme"}}}}`)
	})
	mux.HandleFunc("/notion/users/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ntn" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":"unauthorized","message":"API token is invalid."}`)
			return
		}
		fmt.Fprint(w, `{"name":"Gosha","bot":{"workspace_name":"Acme"}}`)
	})
	mux.HandleFunc("/telegram/{bot}/{method}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("bot") != "bottg" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"ok":false,"description":"Unauthorized"}`)
			return
		}
		method := r.PathValue("method")
		var args map[string]any
		json.NewDecoder(r.Body).Decode(&args)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"username":"gosha_bot"}}`)
		case "getChat":
			fmt.Fprint(w, `{"ok":true,"result":{"id":-1009,"type":"channel","title":"Alerts"}}`)
		case "sendMessage":
			f.sent = append(f.sent, fmt.Sprintf("%v: %v", args["chat_id"], args["text"]))
			fmt.Fprint(w, `{"ok":true,"result":{}}`)
		case "getUpdates":
			var us []string
			if len(f.updates) > 0 {
				us, f.updates = f.updates[0], f.updates[1:]
			}
			fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, strings.Join(us, ","))
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("GET /github/app", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		fmt.Fprintf(w, `{"id":42,"slug":"gosha-test","name":"Gosha Test","owner":{"login":"me","type":"User"},"permissions":%s}`, f.permissions)
	})
	mux.HandleFunc("POST /github/app-manifests/{code}/conversions", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("code") != "code-1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.conversions++
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "slug": "gosha-test", "name": "Gosha Test", "pem": string(f.pem)})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakes) setup(h config.Home, input string, out io.Writer) *Setup {
	u := f.srv.URL
	return &Setup{
		Home: h, P: prompt.New(strings.NewReader(input), out), Red: config.NewRedactor(),
		LinearAPI: u + "/linear/graphql", LinearToken: u + "/linear/token",
		NotionAPI: u + "/notion", TelegramAPI: u + "/telegram", GitHubAPI: u + "/github",
		GitHubWeb: u + "/web", Wait: time.Millisecond,
		Browse: func(string) error { return fmt.Errorf("no browser in tests") },
	}
}

const adminUpdate = `{"update_id":7,"my_chat_member":{"chat":{"id":-1009,"type":"channel","title":"Alerts"},"new_chat_member":{"status":"administrator"}}}`

func TestSetup(t *testing.T) {
	f := newFakes(t)
	h := config.Home(filepath.Join(t.TempDir(), "home"))
	keyFile := filepath.Join(t.TempDir(), "downloaded.pem")
	os.WriteFile(keyFile, f.pem, 0o644)
	f.updates = [][]string{
		{`{"update_id":5,"channel_post":{"chat":{"id":-1001,"type":"channel","title":"Old"}}}`}, // before setup: skipped
		{},
		{`{"update_id":6,"my_chat_member":{"chat":{"id":-1002,"type":"group","title":"A group"},"new_chat_member":{"status":"administrator"}}}`, adminUpdate},
	}
	f.permissions = `{"contents":"write","pull_requests":"write","checks":"read","actions":"read","statuses":"read","metadata":"read","workflows":"write","issues":"read"}`

	input := strings.Join([]string{
		"claude-tok",
		"lin-id", "wrong", // Linear: a bad secret is checked and re-asked
		"", "lin-secret", // Enter keeps the id just typed
		"bad", "ntn",
		"bad", "tg",
		"", // use the channel "Alerts"
		"maybe", "existing", "42", keyFile,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := f.setup(h, input, &out).Run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	t.Log(out.String())

	m, err := config.ReadEnvFile(h.Secrets())
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{config.KeyClaudeToken: "claude-tok", config.KeyLinearClientID: "lin-id",
		config.KeyLinearClientSecret: "lin-secret", config.KeyNotionToken: "ntn", config.KeyTelegramToken: "tg"} {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	g, err := config.LoadGlobal(h)
	if err != nil {
		t.Fatal(err)
	}
	if g.GitHubApp.AppID != 42 || g.Telegram.ChatID != -1009 {
		t.Errorf("global = %+v", g)
	}
	if key, _ := os.ReadFile(h.Path("github-app.pem")); !bytes.Equal(key, f.pem) {
		t.Error("key not copied")
	}
	if fi, _ := os.Stat(h.Dir()); fi.Mode().Perm() != 0o700 {
		t.Errorf("home mode = %v", fi.Mode())
	}
	if len(f.sent) != 1 || f.sent[0] != "-1009: "+testMessage {
		t.Errorf("sent = %q", f.sent)
	}
	s := out.String()
	for _, want := range []string{
		`✗ linear token: 401`, `✓ acts as "Gosha" in Acme`,
		`✗ notion`, `✓ integration "Gosha" in Acme`,
		`✗ telegram getMe: Unauthorized`, `✓ @gosha_bot`, `use the channel "Alerts"?`, `✓ "Alerts" (-1009), test message sent`,
		"one of: new, existing",
		"! Gosha Test has permissions Gosha doesn't need: issues: read",
		"! Gosha Test has the Workflows permission", "https://github.com/settings/apps/gosha-test/permissions",
		"✓ Gosha Test (app 42), key saved", "https://github.com/apps/gosha-test/installations/new",
		"next: gosha doctor, then sudo gosha service install",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	for _, secret := range []string{"claude-tok", "lin-secret", "ntn\n", "wrong"} {
		if strings.Contains(s, secret) {
			t.Errorf("output shows the secret %q", secret)
		}
	}
	if strings.Contains(s, "Old") || strings.Contains(s, "A group") {
		t.Error("offered a channel from before setup, or a group")
	}

	// A re-run where Enter keeps everything checks it all again and
	// changes nothing.
	secretsBefore, _ := os.ReadFile(h.Secrets())
	configBefore, _ := os.ReadFile(h.Config())
	out.Reset()
	if err := f.setup(h, strings.Repeat("\n", 7), &out).Run(context.Background()); err != nil {
		t.Fatalf("re-run: %v\n%s", err, out.String())
	}
	if s, _ := os.ReadFile(h.Secrets()); !bytes.Equal(s, secretsBefore) {
		t.Errorf("re-run changed secrets.env:\n%s", s)
	}
	if c, _ := os.ReadFile(h.Config()); !bytes.Equal(c, configBefore) {
		t.Errorf("re-run changed config.yaml:\n%s", c)
	}
	for _, want := range []string{"[set, Enter keeps it]", `keep the channel "Alerts"?`, "(keep, new, existing) [keep]", "✓ Gosha Test (app 42)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("re-run output lacks %q:\n%s", want, out.String())
		}
	}
	if len(f.sent) != 2 {
		t.Errorf("re-run sent %d test messages", len(f.sent)-1)
	}
}

func TestSetupStopsHalfway(t *testing.T) {
	f := newFakes(t)
	h := config.Home(filepath.Join(t.TempDir(), "home"))
	// Input runs out at Notion: Claude and Linear are already saved.
	err := f.setup(h, "claude-tok\nlin-id\nlin-secret\n", io.Discard).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Notion") {
		t.Fatalf("err = %v", err)
	}
	m, _ := config.ReadEnvFile(h.Secrets())
	if m[config.KeyClaudeToken] != "claude-tok" || m[config.KeyLinearClientSecret] != "lin-secret" || m[config.KeyNotionToken] != "" {
		t.Errorf("secrets = %v", m)
	}
}

func TestMissingPermission(t *testing.T) {
	f := newFakes(t)
	h := config.Home(t.TempDir())
	f.permissions = `{"contents":"read","metadata":"read"}`
	key := filepath.Join(t.TempDir(), "k.pem")
	os.WriteFile(key, f.pem, 0o600)
	var out bytes.Buffer
	s := f.setup(h, "", &out)
	s.out = &out
	_, err := s.checkApp(context.Background(), 42, key)
	if err == nil || !strings.Contains(err.Error(), "missing permissions actions: read, checks: read, contents: write, pull_requests: write, statuses: read") {
		t.Errorf("err = %v", err)
	}
}

var formRe = regexp.MustCompile(`action="([^"]+)"[\s\S]*name="manifest" value="([^"]+)"`)

func TestManifestFlow(t *testing.T) {
	f := newFakes(t)
	h := config.Home(t.TempDir())
	var out bytes.Buffer
	s := f.setup(h, "new\nacme\n", &out)
	var page, action string
	var manifest map[string]any
	var visit func(string) error
	done := make(chan struct{})
	// The page is opened by hand, as when no browser can be.
	s.Browse = func(local string) error {
		go func() { defer close(done); visit(local) }()
		return fmt.Errorf("no display")
	}
	visit = func(local string) error {
		resp, err := http.Get(local)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		page = string(body)
		m := formRe.FindStringSubmatch(page)
		if m == nil {
			return fmt.Errorf("no form in %s", page)
		}
		action = html.UnescapeString(m[1])
		json.Unmarshal([]byte(html.UnescapeString(m[2])), &manifest)
		u, _ := url.Parse(action)
		state := u.Query().Get("state")
		// A callback without the state is refused.
		if resp, _ := http.Get(local + "callback?code=evil&state=nope"); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("forged callback: %s", resp.Status)
		}
		resp, err = http.Get(local + "callback?code=code-1&state=" + state)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Errorf("callback: %v %v", resp, err)
		}
		return nil
	}
	s.out = &out
	if err := s.github(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	<-done
	if !strings.HasPrefix(action, f.srv.URL+"/web/organizations/acme/settings/apps/new?state=") {
		t.Errorf("action = %q", action)
	}
	perms, _ := manifest["default_permissions"].(map[string]any)
	if manifest["public"] != false || manifest["hook_attributes"].(map[string]any)["active"] != false ||
		len(perms) != 6 || perms["contents"] != "write" || perms["workflows"] != nil {
		t.Errorf("manifest = %v", manifest)
	}
	if !strings.HasSuffix(manifest["redirect_url"].(string), "/callback") {
		t.Errorf("redirect_url = %v", manifest["redirect_url"])
	}
	if f.conversions != 1 {
		t.Errorf("conversions = %d", f.conversions)
	}
	key := h.Path("github-app.pem")
	if fi, err := os.Stat(key); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key: %v %v", fi, err)
	}
	if v, _ := config.GlobalValue(h.Config(), "github_app.app_id"); v != "42" {
		t.Errorf("app_id = %q", v)
	}
	if !strings.Contains(out.String(), "ssh -L") {
		t.Errorf("no tunnel hint when the browser can't open:\n%s", out.String())
	}
}
