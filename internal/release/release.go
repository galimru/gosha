// Package release finds, downloads and verifies Gosha's GitHub releases,
// and replaces a binary with one.
package release

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Repo is where releases are published.
const Repo = "galimru/gosha"

// Client talks to the release host.
type Client struct {
	// API is the repo's REST API root, Download the root release assets
	// are under (…/releases/download). Both default to github.com.
	API, Download string
	HTTP          *http.Client
}

func (c *Client) api() string {
	if c.API != "" {
		return c.API
	}
	return "https://api.github.com/repos/" + Repo
}

func (c *Client) download() string {
	if c.Download != "" {
		return c.Download
	}
	return "https://github.com/" + Repo + "/releases/download"
}

func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// Latest is the newest release's tag.
func (c *Client) Latest(ctx context.Context) (string, error) {
	body, err := c.get(ctx, c.api()+"/releases/latest")
	if err != nil {
		return "", fmt.Errorf("latest release: %w", err)
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.TagName == "" {
		return "", fmt.Errorf("latest release: unexpected response")
	}
	return r.TagName, nil
}

// Asset is the archive name for a release and platform, as GoReleaser
// names it.
func Asset(tag, goos, goarch string) string {
	return fmt.Sprintf("gosha_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// Fetch downloads the release's archive for a platform, checks it against
// the release's checksums.txt, and returns the gosha binary inside.
func (c *Client) Fetch(ctx context.Context, tag, goos, goarch string) ([]byte, error) {
	asset := Asset(tag, goos, goarch)
	base := c.download() + "/" + tag + "/"
	sums, err := c.get(ctx, base+"checksums.txt")
	if err != nil {
		return nil, err
	}
	want, err := checksum(sums, asset)
	if err != nil {
		return nil, err
	}
	archive, err := c.get(ctx, base+asset)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("%s: checksum mismatch: the download is corrupt or was tampered with", asset)
	}
	return extract(archive)
}

// checksum finds asset's sha256 in a checksums.txt.
func checksum(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", asset)
}

// extract returns the gosha binary from a release archive.
func extract(archive []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the archive has no gosha binary")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "gosha" {
			return io.ReadAll(tr)
		}
	}
}

// Replace swaps the binary at path for data atomically: the new binary is
// written next to it and renamed over it, keeping its mode and owner.
func Replace(path string, data []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gosha-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), fi.Mode().Perm()|0o111); err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		if err := os.Chown(tmp.Name(), int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}

// Writable reports whether files can be created in dir.
func Writable(dir string) error {
	f, err := os.CreateTemp(dir, ".gosha-write-test-*")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}

// Compare orders two vX.Y.Z tags: -1, 0 or 1. A tag that doesn't parse
// sorts first.
func Compare(a, b string) int {
	pa, oka := parse(a)
	pb, okb := parse(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for i := range 3 {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parse(tag string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
