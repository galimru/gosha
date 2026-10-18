// Command gosha is the Gosha CLI and daemon (DESIGN §11.6).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/pipeline"
	"github.com/galimru/gosha/internal/store"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

// geteuid is swapped in tests.
var geteuid = os.Geteuid

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := &app{}
	root := a.rootCmd()
	err := root.ExecuteContext(ctx)
	a.close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gosha:", a.redact(err.Error()))
		os.Exit(1)
	}
}

// app holds what commands share. Everything is set up lazily, so commands
// like doctor work before Gosha is configured.
type app struct {
	homeFlag string
	logLevel string

	home    config.Home
	log     *slog.Logger
	red     *config.Redactor
	closers []func()
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "gosha",
		Short:         "Gosha turns Ready Linear tickets into merged pull requests",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.init()
		},
	}
	root.PersistentFlags().StringVar(&a.homeFlag, "home", "", "Gosha home directory (default $GOSHA_HOME or ~/.gosha)")
	root.PersistentFlags().StringVar(&a.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	root.AddCommand(
		a.addCmd(), a.removeCmd(), a.listCmd(), a.validateCmd(), a.doctorCmd(),
		a.secretsCmd(), a.statusCmd(), a.runCmd(), a.daemonCmd(), a.contextCmd(),
	)
	return root
}

func (a *app) init() error {
	var err error
	if a.home, err = config.ResolveHome(a.homeFlag); err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(a.logLevel)); err != nil {
		return fmt.Errorf("--log-level: %w", err)
	}
	a.red = config.NewRedactor()
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	// Log to the file only once the home exists; commands never create it
	// just to log.
	if fi, err := os.Stat(a.home.Dir()); err == nil && fi.IsDir() {
		if f, err := openLog(a.home.LogFile()); err == nil {
			a.closers = append(a.closers, func() { f.Close() })
			h = teeHandler{h, slog.NewJSONHandler(f, opts)}
		}
	}
	a.log = slog.New(a.red.Handler(h))
	return nil
}

func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func (a *app) close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
}

func (a *app) redact(s string) string {
	if a.red == nil {
		return s
	}
	return a.red.Redact(s)
}

// env loads the shared secrets and global config and opens the store, the
// GitHub App and the Telegram feed.
func (a *app) env(ctx context.Context) (*pipeline.Env, error) {
	if fi, err := os.Stat(a.home.Dir()); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("no Gosha home at %s: create it with config.yaml (DESIGN §5.1), or pass --home", a.home.Dir())
	}
	secrets, err := config.LoadSecrets(a.home, "", a.red)
	if err != nil {
		return nil, err
	}
	botToken, err := secrets.Require(config.KeyTelegramToken)
	if err != nil {
		return nil, err
	}
	g, err := config.LoadGlobal(a.home)
	if err != nil {
		return nil, err
	}
	app, err := github.NewApp(g.GitHubApp.AppID, g.GitHubApp.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, a.home.DB(), version)
	if err != nil {
		return nil, err
	}
	a.closers = append(a.closers, func() { st.Close() })
	tg := notify.NewTelegram(botToken, g.Telegram.ChatID, a.red, a.log)
	a.closers = append(a.closers, func() {
		// Give queued messages a moment; a Telegram outage must not hang exit.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := tg.Flush(ctx); err != nil {
			a.log.Warn("telegram", "err", err)
		}
		tg.Close()
	})
	return &pipeline.Env{Home: a.home, Global: g, App: app, Store: st, Notify: tg, Log: a.log, Redactor: a.red}, nil
}

// refuseRoot stops commands that run agents from running as root (DESIGN §11.2).
func refuseRoot() error {
	if geteuid() == 0 {
		return errors.New("refusing to run as root: agents execute commands derived from ticket text. " +
			"Run Gosha as a regular user (e.g. create one with `adduser deploy`)")
	}
	return nil
}

// teeHandler writes every record to two handlers.
type teeHandler struct{ a, b slog.Handler }

func (t teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return t.a.Enabled(ctx, l) || t.b.Enabled(ctx, l)
}

func (t teeHandler) Handle(ctx context.Context, r slog.Record) error {
	return errors.Join(t.a.Handle(ctx, r.Clone()), t.b.Handle(ctx, r))
}

func (t teeHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return teeHandler{t.a.WithAttrs(as), t.b.WithAttrs(as)}
}

func (t teeHandler) WithGroup(n string) slog.Handler {
	return teeHandler{t.a.WithGroup(n), t.b.WithGroup(n)}
}
