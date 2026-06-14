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
