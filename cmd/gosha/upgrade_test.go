package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/galimru/gosha/internal/release"
	"github.com/galimru/gosha/internal/service"
)

func fakeReleases(t *testing.T) {
	t.Helper()
	var arc bytes.Buffer
	zw := gzip.NewWriter(&arc)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: "gosha", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg})
	tw.Write([]byte("new"))
	tw.Close()
	zw.Close()
	asset := release.Asset("v0.2.0", runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/releases/latest", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"tag_name":"v0.2.0"}`) })
	mux.HandleFunc("/dl/v0.2.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(arc.Bytes()), asset)
	})
	mux.HandleFunc("/dl/v0.2.0/"+asset, func(w http.ResponseWriter, _ *http.Request) { w.Write(arc.Bytes()) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := releases
	releases = &release.Client{API: srv.URL + "/api", Download: srv.URL + "/dl"}
	t.Cleanup(func() { releases = old })
}

func TestUpgrade(t *testing.T) {
	fakeReleases(t)
	bin := filepath.Join(t.TempDir(), "gosha")
	os.WriteFile(bin, []byte("old"), 0o755)
	defer func(v string, e func() (string, error), m func() (service.Manager, error)) {
		version, executable, hostManager = v, e, m
	}(version, executable, hostManager)
	executable = func() (string, error) { return bin, nil }
	hostManager = func() (service.Manager, error) { return nil, service.ErrUnsupported }

	version = "dev"
	if _, err := runCLI(t, "upgrade"); err == nil || !strings.Contains(err.Error(), "dev build") {
		t.Errorf("dev build: %v", err)
	}
	version = "v0.1.0"
	if out, err := runCLI(t, "upgrade", "--check"); err != nil || !strings.Contains(out, "v0.2.0 is available") {
		t.Errorf("--check: %q, %v", out, err)
	}
	if data, _ := os.ReadFile(bin); string(data) != "old" {
		t.Fatal("--check replaced the binary")
	}
	if out, err := runCLI(t, "upgrade"); err != nil || !strings.Contains(out, "v0.1.0 → v0.2.0") {
		t.Fatalf("upgrade: %q, %v", out, err)
	}
	if data, _ := os.ReadFile(bin); string(data) != "new" {
		t.Errorf("binary = %q", data)
	}
	version = "v0.2.0"
	if out, err := runCLI(t, "upgrade"); err != nil || !strings.Contains(out, "up to date") {
		t.Errorf("up to date: %q, %v", out, err)
	}
}
