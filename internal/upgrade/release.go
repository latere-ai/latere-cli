// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"latere.ai/x/pkg/otel"
)

// maxArchiveBytes caps how much we read from a release archive, a guard
// against a hostile or corrupt download exhausting memory.
const maxArchiveBytes = 200 << 20 // 200 MiB

// githubBase is the host serving releases. It is a var so tests can point it
// at a local server; production always uses github.com.
var githubBase = "https://github.com"

func httpClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: otel.Transport(nil)}
}

// downloadClient is the client for the binary download. It deliberately
// carries no tight end-to-end Timeout: http.Client.Timeout is end-to-end
// (it also bounds reading the response body), so a 30s client timeout would
// silently cap the caller's download context and make a normal-sized binary
// download on a slow link fail as a spurious "timeout". The bounded context
// the callers pass (performAutoUpgrade/Run) is the single source of truth for
// the download deadline. A generous fallback guards against a future caller
// that forgets to set one, so the process can never hang forever.
func downloadClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute, Transport: otel.Transport(nil)}
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
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return "", fmt.Errorf("latest-release lookup from %s returned status %d; expected a redirect", url, resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("no redirect from %s (status %d)", url, resp.StatusCode)
	}
	target, err := req.URL.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("unexpected latest-release redirect %q: %w", loc, err)
	}
	// Resolve relative references and read the tag from the release path,
	// excluding tracking queries and fragments from the version itself.
	tag, releasePath := strings.CutPrefix(target.Path, "/"+repoSlug+"/releases/tag/")
	if target.Scheme != req.URL.Scheme || !strings.EqualFold(target.Host, req.URL.Host) || target.User != nil ||
		!releasePath || strings.ContainsAny(tag, "/\\?#") || !isRelease(tag) {
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
		client = downloadClient()
	}
	asset := assetName(tag)
	base := githubBase + "/" + repoSlug + "/releases/download/" + tag + "/"

	archive, err := fetch(ctx, client, base+asset, "Downloading "+asset)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	sums, err := fetch(ctx, client, base+"checksums.txt", "")
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

// progressDst is where the download bar is drawn. A var so tests can
// capture it; the isTerminalStderr gate decides whether it draws at all.
var progressDst io.Writer = os.Stderr

// fetch GETs url. With a non-empty label and an interactive stderr it
// renders a single-line progress bar while the body streams.
func fetch(ctx context.Context, client *http.Client, url, label string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "latere-cli")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	body := io.LimitReader(resp.Body, maxArchiveBytes)
	if label != "" && isTerminalStderr() {
		p := &progressReader{r: body, w: progressDst, label: label, total: resp.ContentLength}
		defer p.clear()
		body = p
	}
	return io.ReadAll(body)
}

// progressReader renders a one-line download bar as reads flow through
// it, redrawing only when the rendered line changes. The caller clears
// the line when the download ends so following output starts clean.
type progressReader struct {
	r     io.Reader
	w     io.Writer
	label string
	total int64
	done  int64
	last  string
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	p.render()
	return n, err
}

func (p *progressReader) render() {
	var line string
	if p.total > 0 {
		pct := min(p.done*100/p.total, 100)
		const cells = 20
		filled := int(pct) * cells / 100
		bar := strings.Repeat("▇", filled) + strings.Repeat("─", cells-filled)
		line = fmt.Sprintf("%s  %s %3d%% (%s / %s)", p.label, bar, pct, fmtMB(p.done), fmtMB(p.total))
	} else {
		line = fmt.Sprintf("%s  %s", p.label, fmtMB(p.done))
	}
	if line == p.last {
		return
	}
	p.last = line
	fprintf(p.w, "\r\033[K%s", line)
}

// clear erases the bar so the next message starts on a clean line.
func (p *progressReader) clear() {
	if p.last != "" {
		fprint(p.w, "\r\033[K")
	}
}

func fmtMB(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
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
	return extractBinaryWithLimit(archive, maxArchiveBytes)
}

func extractBinaryWithLimit(archive []byte, maxBinaryBytes int64) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
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
		if hdr.Size == 0 {
			return nil, fmt.Errorf("empty latere binary in archive")
		}
		if hdr.Size > maxBinaryBytes {
			return nil, fmt.Errorf("latere binary is too large (%d bytes; maximum %d bytes)", hdr.Size, maxBinaryBytes)
		}
		buf := &bytes.Buffer{}
		if _, err := io.Copy(buf, io.LimitReader(tr, maxBinaryBytes)); err != nil { //nolint:gosec // bounded by LimitReader
			return nil, fmt.Errorf("extract binary: %w", err)
		}
		// Closing a gzip reader does not validate its checksum or size.
		// Reach EOF before accepting the binary, bounding the remaining
		// expanded data so an archive cannot force an unbounded drain.
		n, err := io.Copy(io.Discard, io.LimitReader(gz, maxArchiveBytes+1))
		if err != nil {
			return nil, fmt.Errorf("verify archive: %w", err)
		}
		if n > maxArchiveBytes {
			return nil, fmt.Errorf("remaining expanded archive exceeds %d bytes", maxArchiveBytes)
		}
		return buf.Bytes(), nil
	}
	return nil, fmt.Errorf("latere binary not found in archive")
}
