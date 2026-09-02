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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChecksumFor(t *testing.T) {
	sums := "aaaa  latere_0.3.0_linux_amd64.tar.gz\n" +
		"bbbb  latere_0.3.0_darwin_arm64.tar.gz\n"
	if got := checksumFor(sums, "latere_0.3.0_darwin_arm64.tar.gz"); got != "bbbb" {
		t.Errorf("checksumFor = %q, want bbbb", got)
	}
	if got := checksumFor(sums, "missing.tar.gz"); got != "" {
		t.Errorf("checksumFor(missing) = %q, want empty", got)
	}
}

func TestAssetName(t *testing.T) {
	// Filenames drop the leading v even though the tag carries it.
	want := fmt.Sprintf("latere_0.3.0_%s_%s.", runtime.GOOS, runtime.GOARCH)
	got := assetName("v0.3.0")
	if got[:len(want)] != want {
		t.Errorf("assetName = %q, want prefix %q", got, want)
	}
}

// makeTarGz builds a .tar.gz containing the named entries -> contents.
func makeTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBinary(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"LICENSE":   "license text",
		"latere":    "BINARY-BYTES",
		"README.md": "readme",
	})
	got, err := extractBinary(archive)
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if string(got) != "BINARY-BYTES" {
		t.Errorf("extractBinary = %q, want BINARY-BYTES", got)
	}

	if _, err := extractBinary(makeTarGz(t, map[string]string{"other": "x"})); err == nil {
		t.Error("expected error when latere binary is absent")
	}
}

func TestResolveLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+repoSlug+"/releases/tag/v9.9.9", http.StatusFound)
	}))
	defer srv.Close()
	old := githubBase
	githubBase = srv.URL
	defer func() { githubBase = old }()

	tag, err := ResolveLatest(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("ResolveLatest: %v", err)
	}
	if tag != "v9.9.9" {
		t.Errorf("tag = %q, want v9.9.9", tag)
	}
}

func TestDownloadBinaryVerifiesChecksum(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"latere": "NEW-BINARY"})
	asset := assetName("v9.9.9")
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)

	mux := http.NewServeMux()
	mux.HandleFunc("/"+repoSlug+"/releases/download/v9.9.9/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})
	mux.HandleFunc("/"+repoSlug+"/releases/download/v9.9.9/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, checksums)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := githubBase
	githubBase = srv.URL
	defer func() { githubBase = old }()

	got, err := DownloadBinary(context.Background(), srv.Client(), "v9.9.9")
	if err != nil {
		t.Fatalf("DownloadBinary: %v", err)
	}
	if string(got) != "NEW-BINARY" {
		t.Errorf("DownloadBinary = %q, want NEW-BINARY", got)
	}
}

// TestDownloadClientHasNoTightTimeout pins the fix for the bug where a 30s
// http.Client.Timeout silently capped the caller's 120s download context.
// http.Client.Timeout is end-to-end (it bounds reading the body too), so the
// download client must not re-introduce a cap shorter than the download
// context the callers grant. A regression that reset it to 30s would fail here.
func TestDownloadClientHasNoTightTimeout(t *testing.T) {
	if to := downloadClient().Timeout; to != 0 && to < 120*time.Second {
		t.Errorf("downloadClient timeout = %s, want 0 or >= 120s so the bounded context governs the download", to)
	}
}

// TestDownloadBinaryHonorsContextDeadline verifies the caller's context, not a
// hardcoded client timeout, bounds the download: a slow server past the
// context deadline makes DownloadBinary fail, and a short delay within the
// deadline succeeds.
func TestDownloadBinaryHonorsContextDeadline(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"latere": "NEW-BINARY"})
	asset := assetName("v9.9.9")
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)

	// The first request is still sleeping when the second phase sets a new
	// delay, so the field is read from a server goroutine while the test
	// writes it. Atomic, not a plain variable: the race detector reports the
	// plain form and `make test-race` is the gate that runs it.
	var delay atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/"+repoSlug+"/releases/download/v9.9.9/"+asset, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(delay.Load()))
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/"+repoSlug+"/releases/download/v9.9.9/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, checksums)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := githubBase
	githubBase = srv.URL
	defer func() { githubBase = old }()

	// Server slower than the context deadline -> download fails on the context.
	delay.Store(int64(200 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := DownloadBinary(ctx, downloadClient(), "v9.9.9"); err == nil {
		t.Fatal("expected download to fail when the context deadline elapses")
	}

	// Server within the deadline -> download succeeds via the bounded context.
	delay.Store(0)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	got, err := DownloadBinary(ctx2, downloadClient(), "v9.9.9")
	if err != nil {
		t.Fatalf("DownloadBinary within deadline: %v", err)
	}
	if string(got) != "NEW-BINARY" {
		t.Errorf("DownloadBinary = %q, want NEW-BINARY", got)
	}
}

func TestDownloadBinaryRejectsBadChecksum(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"latere": "NEW-BINARY"})
	asset := assetName("v9.9.9")

	mux := http.NewServeMux()
	mux.HandleFunc("/"+repoSlug+"/releases/download/v9.9.9/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})
	mux.HandleFunc("/"+repoSlug+"/releases/download/v9.9.9/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", "deadbeef", asset)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := githubBase
	githubBase = srv.URL
	defer func() { githubBase = old }()

	if _, err := DownloadBinary(context.Background(), srv.Client(), "v9.9.9"); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

func TestProgressReaderRendersBar(t *testing.T) {
	var buf bytes.Buffer
	p := &progressReader{
		r:     bytes.NewReader(bytes.Repeat([]byte("x"), 4<<20)),
		w:     &buf,
		label: "Downloading test.tar.gz",
		total: 4 << 20,
	}
	if _, err := io.Copy(io.Discard, p); err != nil {
		t.Fatal(err)
	}
	p.clear()
	out := buf.String()
	if !strings.Contains(out, "Downloading test.tar.gz") {
		t.Errorf("missing label: %q", out)
	}
	if !strings.Contains(out, "100% (4.0 MB / 4.0 MB)") {
		t.Errorf("missing final percentage: %q", out)
	}
	if !strings.Contains(out, "▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇") {
		t.Errorf("missing full bar: %q", out)
	}
	if !strings.HasSuffix(out, "\r\033[K") {
		t.Errorf("clear must erase the line: %q", out[len(out)-20:])
	}
}

func TestProgressReaderUnknownTotal(t *testing.T) {
	var buf bytes.Buffer
	p := &progressReader{
		r:     bytes.NewReader(bytes.Repeat([]byte("x"), 1<<20)),
		w:     &buf,
		label: "Downloading test.tar.gz",
		total: -1, // ContentLength unknown
	}
	if _, err := io.Copy(io.Discard, p); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "1.0 MB") || strings.Contains(out, "%") {
		t.Errorf("unknown total must show bytes only: %q", out)
	}
}

func TestProgressReaderRedrawsOnlyOnChange(t *testing.T) {
	var buf bytes.Buffer
	p := &progressReader{r: bytes.NewReader(make([]byte, 100)), w: &buf, label: "dl", total: 1 << 30}
	// 100 bytes of a 1 GiB total: percent and MB stay at zero across reads.
	tmp := make([]byte, 10)
	for {
		if _, err := p.Read(tmp); err != nil {
			break
		}
	}
	if n := strings.Count(buf.String(), "dl"); n != 1 {
		t.Errorf("expected a single draw for unchanged content, got %d", n)
	}
}

// The bar draws during DownloadBinary when stderr is interactive.
func TestDownloadBinaryDrawsProgress(t *testing.T) {
	oldTTY, oldDst := isTerminalStderr, progressDst
	var drawn bytes.Buffer
	isTerminalStderr = func() bool { return true }
	progressDst = &drawn
	t.Cleanup(func() { isTerminalStderr, progressDst = oldTTY, oldDst })

	bin := []byte("#!fake binary")
	archive := makeTarGz(t, map[string]string{"latere": string(bin)})
	sum := sha256.Sum256(archive)
	asset := assetName("v9.9.9")

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/latere-ai/latere-cli/releases/download/v9.9.9/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/latere-ai/latere-cli/releases/download/v9.9.9/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
	})

	oldBase := githubBase
	githubBase = srv.URL
	t.Cleanup(func() { githubBase = oldBase })

	got, err := DownloadBinary(context.Background(), srv.Client(), "v9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bin) {
		t.Errorf("binary mismatch")
	}
	if !strings.Contains(drawn.String(), "Downloading "+asset) {
		t.Errorf("no progress drawn: %q", drawn.String())
	}
}
