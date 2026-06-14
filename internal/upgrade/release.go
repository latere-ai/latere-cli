package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path"
	"runtime"
	"strings"
	"time"
)

// maxArchiveBytes caps how much we read from a release archive, a guard
// against a hostile or corrupt download exhausting memory.
const maxArchiveBytes = 200 << 20 // 200 MiB

// githubBase is the host serving releases. It is a var so tests can point it
// at a local server; production always uses github.com.
var githubBase = "https://github.com"

func httpClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// ResolveLatest returns the newest published release tag (e.g. "v0.2.30") by
// reading the redirect target of github.com/<repo>/releases/latest. See the
// package doc for why this avoids api.github.com.
func ResolveLatest(ctx context.Context, client *http.Client) (string, error) {
	if client == nil {
		client = httpClient()
	}
	// Copy the client so we can stop it following the redirect without
	// mutating the caller's client.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	url := githubBase + "/" + repoSlug + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "latere-cli")
	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("no redirect from %s (status %d)", url, resp.StatusCode)
	}
	// .../releases/tag/v0.2.30 -> v0.2.30
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if tag == "" || !strings.Contains(loc, "/releases/tag/") {
		return "", fmt.Errorf("unexpected latest-release redirect: %s", loc)
	}
	return tag, nil
}

// assetName returns the release archive filename for the running platform.
// goreleaser strips the leading "v" from the version in filenames.
func assetName(tag string) string {
	version := strings.TrimPrefix(tag, "v")
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("latere_%s_%s_%s.%s", version, runtime.GOOS, runtime.GOARCH, ext)
}

// DownloadBinary fetches the release archive for tag, verifies it against the
// release checksums.txt, and returns the extracted latere binary.
func DownloadBinary(ctx context.Context, client *http.Client, tag string) ([]byte, error) {
	if client == nil {
		client = httpClient()
	}
	asset := assetName(tag)
	base := githubBase + "/" + repoSlug + "/releases/download/" + tag + "/"

	archive, err := fetch(ctx, client, base+asset)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	sums, err := fetch(ctx, client, base+"checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}
	want := checksumFor(string(sums), asset)
	if want == "" {
		return nil, fmt.Errorf("no checksum for %s in checksums.txt", asset)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s", asset)
	}
	return extractBinary(archive)
}

func fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "latere-cli")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxArchiveBytes))
}

// checksumFor returns the hex sha256 recorded for asset in a checksums.txt
// body (lines of "<hex>  <filename>").
func checksumFor(sums, asset string) string {
	for line := range strings.SplitSeq(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0]
		}
	}
	return ""
}

// extractBinary returns the "latere" binary from a .tar.gz release archive.
// It matches on the entry's base name and writes to a caller-controlled
// buffer, never honouring the archive's own path (no tar-slip).
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if path.Base(hdr.Name) != "latere" {
			continue
		}
		buf := &bytes.Buffer{}
		if _, err := io.Copy(buf, io.LimitReader(tr, maxArchiveBytes)); err != nil { //nolint:gosec // bounded by LimitReader
			return nil, fmt.Errorf("extract binary: %w", err)
		}
		return buf.Bytes(), nil
	}
	return nil, fmt.Errorf("latere binary not found in archive")
}
