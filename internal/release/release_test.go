package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	for name, data := range map[string][]byte{"README.md": []byte("readme"), "gosha": binary} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg})
		tw.Write(data)
	}
	tw.Close()
	zw.Close()
	return b.Bytes()
}

// server publishes v0.3.0 for linux/arm64; tamper corrupts the archive.
func server(t *testing.T, tamper bool) *Client {
	t.Helper()
	arc := archive(t, []byte("new binary"))
	sum := sha256.Sum256(arc)
	if tamper {
		arc = append(arc, 0)
	}
	asset := Asset("v0.3.0", "linux", "arm64")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.3.0"}`)
	})
	mux.HandleFunc("/dl/v0.3.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%x  gosha_0.3.0_darwin_arm64.tar.gz\n%x  %s\n", [32]byte{}, sum, asset)
	})
	mux.HandleFunc("/dl/v0.3.0/"+asset, func(w http.ResponseWriter, _ *http.Request) { w.Write(arc) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Client{API: srv.URL + "/api", Download: srv.URL + "/dl"}
}

func TestFetch(t *testing.T) {
	c := server(t, false)
	ctx := context.Background()
	tag, err := c.Latest(ctx)
	if err != nil || tag != "v0.3.0" {
		t.Fatalf("Latest = %q, %v", tag, err)
	}
	bin, err := c.Fetch(ctx, tag, "linux", "arm64")
	if err != nil || string(bin) != "new binary" {
		t.Fatalf("Fetch = %q, %v", bin, err)
	}
	if _, err := c.Fetch(ctx, tag, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Errorf("missing platform: %v", err)
	}
}

func TestFetchRejectsBadChecksum(t *testing.T) {
	c := server(t, true)
	if _, err := c.Fetch(context.Background(), "v0.3.0", "linux", "arm64"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered archive: %v", err)
	}
}

func TestReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gosha")
	if err := os.WriteFile(path, []byte("old"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	fi, _ := os.Stat(path)
	if string(data) != "new" || fi.Mode().Perm() != 0o751 {
		t.Errorf("replaced: %q, %v", data, fi.Mode())
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temp files left: %v", entries)
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.2.0", "v0.10.0", -1}, {"v0.3.1", "v0.3.0", 1}, {"v1.0.0", "v1.0.0", 0}, {"dev", "v0.1.0", -1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d", c.a, c.b, got)
		}
	}
}
