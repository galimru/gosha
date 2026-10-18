package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/galimru/gosha/internal/gitx"
)

func writeKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallationToken(t *testing.T) {
	var mints atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("%s without an App JWT", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app":
			w.Write([]byte(`{"id": 42, "slug": "goshabot", "name": "GoshaBot"}`))
		case "/repos/o/r/installation":
			w.Write([]byte(`{"id": 7}`))
		case "/repos/o/missing/installation":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message": "Not Found"}`))
		case "/app/installations/7/access_tokens":
			mints.Add(1)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"token": "ghs_test", "expires_at": "` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	app, err := NewApp(42, writeKey(t), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	info, err := app.Info(ctx)
	if err != nil || info.BotLogin() != "goshabot[bot]" {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	inst, err := app.Installation(ctx, gitx.GitHubRepo{Owner: "o", Name: "r"})
	if err != nil || inst.ID != 7 {
		t.Fatalf("Installation = %+v, %v", inst, err)
	}
	for range 2 {
		tok, err := inst.Token(ctx)
		if err != nil || tok != "ghs_test" {
			t.Fatalf("Token = %q, %v", tok, err)
		}
	}
	if mints.Load() != 1 {
		t.Errorf("token minted %d times, want 1 (cached)", mints.Load())
	}
	if _, err := app.Installation(ctx, gitx.GitHubRepo{Owner: "o", Name: "missing"}); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("missing installation: %v", err)
	}
}
