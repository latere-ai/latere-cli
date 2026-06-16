package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
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

	var delay time.Duration
	mux := http.NewServeMux()
	mux.HandleFunc("/"+repoSlug+"/releases/download/v9.9.9/"+asset, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
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

	// Server slower than the context deadline -> download fails on the context.
	delay = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := DownloadBinary(ctx, downloadClient(), "v9.9.9"); err == nil {
		t.Fatal("expected download to fail when the context deadline elapses")
	}

	// Server within the deadline -> download succeeds via the bounded context.
	delay = 0
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
