package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/github"
)

func (s *Setup) github(ctx context.Context) error {
	keyFile, err := config.GlobalValue(s.Home.Config(), "github_app.private_key_file")
	if err != nil {
		return err
	}
	if keyFile == "" {
		keyFile = "github-app.pem"
	}
	key := s.Home.Path(keyFile)
	cur, err := config.GlobalValue(s.Home.Config(), "github_app.app_id")
	if err != nil {
		return err
	}
	curID, _ := strconv.ParseInt(cur, 10, 64)
	choices, def := "new, existing", "new"
	if _, err := os.Stat(key); err == nil && curID > 0 {
		choices, def = "keep, new, existing", "keep"
	}

	for {
		how, err := s.P.Ask("  create a new app, or use one you have? ("+choices+")", def, func(a string) error {
			if !strings.Contains(", "+choices+",", ", "+a+",") {
				return fmt.Errorf("one of: %s", choices)
			}
			return nil
		})
		if err != nil {
			return err
		}
		var info *github.AppInfo
		switch how {
		case "keep":
			info, err = s.checkApp(ctx, curID, key)
		case "new":
			info, err = s.createApp(ctx, key)
		case "existing":
			info, err = s.existingApp(ctx, key)
		}
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			s.fail(err)
			continue
		}
		if err := s.setGlobal("github_app.app_id", strconv.FormatInt(info.ID, 10)); err != nil {
			return err
		}
		s.ok("%s (app %d), key saved", info.Name, info.ID)
		s.say("install it on the repos Gosha works on: https://github.com/apps/%s/installations/new", info.Slug)
		return nil
	}
}

// checkApp looks the App up with its key and compares its permissions with
// what Gosha needs (DESIGN §8.6).
func (s *Setup) checkApp(ctx context.Context, id int64, key string) (*github.AppInfo, error) {
	var opts []github.Option
	if s.GitHubAPI != "" {
		opts = append(opts, github.WithBaseURL(s.GitHubAPI))
	}
	app, err := github.NewApp(id, key, opts...)
	if err != nil {
		return nil, err
	}
	info, err := app.Info(ctx)
	if err != nil {
		return nil, err
	}
	pc := github.CheckPermissions(info.Permissions)
	if len(pc.Missing) > 0 {
		return nil, fmt.Errorf("%s is missing permissions %s: grant them at %s/permissions",
			info.Name, strings.Join(pc.Missing, ", "), info.SettingsURL)
	}
	if len(pc.Extra) > 0 {
		s.warn("%s has permissions Gosha doesn't need: %s", info.Name, strings.Join(pc.Extra, ", "))
	}
	if pc.Workflows {
		s.warn("%s has the Workflows permission, so agents could change CI and deploy workflows: remove it at %s/permissions unless you need it",
			info.Name, info.SettingsURL)
	}
	return info, nil
}

// existingApp asks for an App's id and private key, checks them, and copies
// the key to key.
func (s *Setup) existingApp(ctx context.Context, key string) (*github.AppInfo, error) {
	s.say("The App ID is on the app's settings page; a private key can be generated there too.")
	idText, err := s.P.Ask("  App ID", "", func(a string) error {
		if n, err := strconv.ParseInt(a, 10, 64); err != nil || n <= 0 {
			return errors.New("must be a number")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	id, _ := strconv.ParseInt(idText, 10, 64)
	src, err := s.P.Ask("  private key file (.pem)", "", nil)
	if err != nil {
		return nil, err
	}
	if rest, ok := strings.CutPrefix(src, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			src = filepath.Join(home, rest)
		}
	}
	if src, err = filepath.Abs(src); err != nil {
		return nil, err
	}
	info, err := s.checkApp(ctx, id, src)
	if err != nil {
		return nil, err
	}
	if src == key {
		return info, os.Chmod(key, 0o600)
	}
	pem, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	return info, config.WriteFile(key, pem, 0o600)
}

// manifestPage posts the manifest to GitHub's new-app form.
var manifestPage = template.Must(template.New("").Parse(`<!doctype html>
<meta charset="utf-8">
<title>Create the Gosha GitHub App</title>
<form id="f" method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<p>Taking you to GitHub… <button>Continue</button></p>
</form>
<script>document.getElementById("f").submit()</script>
`))

const donePage = `<!doctype html>
<meta charset="utf-8">
<title>Gosha</title>
<p>Gosha has the new app. You can close this tab and go back to the terminal.</p>
`

// createApp creates an App with GitHub's manifest flow: a page on
// 127.0.0.1 posts the manifest to GitHub, the operator names the app and
// clicks Create, and GitHub redirects back with a code that is exchanged
// for the App's id and private key. The key is written to key.
func (s *Setup) createApp(ctx context.Context, key string) (*github.AppInfo, error) {
	org, err := s.P.Optional("  organisation to create it on (Enter for your own account)")
	if err != nil {
		return nil, err
	}
	web := s.GitHubWeb
	if web == "" {
		web = "https://github.com"
	}
	action := web + "/settings/apps/new"
	if org != "" {
		action = web + "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	local := fmt.Sprintf("http://127.0.0.1:%d/", port)
	b := make([]byte, 16)
	rand.Read(b)
	state := hex.EncodeToString(b)
	manifest, err := json.Marshal(github.Manifest("gosha", local+"callback"))
	if err != nil {
		return nil, err
	}

	codes := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		manifestPage.Execute(w, map[string]string{"Action": action + "?state=" + state, "Manifest": string(manifest)})
	})
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if r.URL.Query().Get("state") != state || code == "" {
			http.Error(w, "unexpected callback: start again from the terminal", http.StatusBadRequest)
			return
		}
		select {
		case codes <- code:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, donePage)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	s.say("On the page that opens, name the app (names are unique on GitHub, e.g. gosha-<your login>)")
	s.say("and click Create. Page: %s", local)
	browse := s.Browse
	if browse == nil {
		browse = openBrowser
	}
	if err := browse(local); err != nil {
		s.say("No browser could be opened (%v). Open the page yourself; from another machine,", err)
		s.say("forward the port first: ssh -L %d:127.0.0.1:%d <this host>", port, port)
	}
	s.say("Waiting for GitHub… (Ctrl-C to stop)")
	var code string
	select {
	case code = <-codes:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	var opts []github.Option
	if s.GitHubAPI != "" {
		opts = append(opts, github.WithBaseURL(s.GitHubAPI))
	}
	app, err := github.ConvertManifest(ctx, code, opts...)
	if err != nil {
		return nil, err
	}
	if err := config.WriteFile(key, []byte(app.PEM), 0o600); err != nil {
		return nil, err
	}
	// Save the id now: the key is useless without it.
	if err := s.setGlobal("github_app.app_id", strconv.FormatInt(app.ID, 10)); err != nil {
		return nil, err
	}
	return s.checkApp(ctx, app.ID, key)
}

// openBrowser opens url with the system's opener. It fails where no
// browser can be shown: over SSH, or on Linux without a display.
func openBrowser(url string) error {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return errors.New("running over SSH")
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	} else if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("no display")
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
