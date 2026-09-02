// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInstallScriptResolvesLatestWithoutGitHubAPI guards the China install
// regression: api.github.com caps unauthenticated callers at 60 req/hour per
// IP, so behind shared carrier-grade NAT GitHub answers 403 and the old script
// bailed with "could not resolve latest release". The script must now resolve
// "latest" from the github.com releases redirect, never the rate-limited API.
func TestInstallScriptResolvesLatestWithoutGitHubAPI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is a POSIX shell script")
	}
	for _, bin := range []string{"sh", "tar", "uname", "install", "mktemp"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("required tool %q not available", bin)
		}
	}

	_, thisFile, _, _ := runtime.Caller(0)
	scriptPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "install.sh")

	tmp := t.TempDir()
	const wantVersion = "v9.9.9"

	// The release archive the fake curl "downloads": a lone executable named
	// `latere` that prints a version string when run with --version.
	tarPath := filepath.Join(tmp, "release.tar.gz")
	writeReleaseTarball(t, tarPath, "#!/bin/sh\necho \"latere "+wantVersion+" (test build)\"\n")

	// Fake curl shim placed first on PATH:
	//   - api.github.com            -> exit 22 (HTTP 403). Regression guard:
	//     if the script ever calls the API again this makes the test fail.
	//   - .../releases/latest (-I)  -> 302 with a Location header to the tag.
	//   - .../releases/download/... -> copy the prebuilt tarball to -o target.
	//   - checksums.txt             -> exit 1 so checksum verification is
	//     skipped (the script tolerates a missing checksums file).
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	curlShim := `#!/bin/sh
out=""
url=""
prev=""
for a in "$@"; do
  case "$prev" in -o) out="$a" ;; esac
  case "$a" in https://*) url="$a" ;; esac
  prev="$a"
done
case "$url" in
  *api.github.com*)             exit 22 ;;
  */releases/latest)            printf 'HTTP/2 302\r\nlocation: https://github.com/latere-ai/latere-cli/releases/tag/` + wantVersion + `\r\n\r\n'; exit 0 ;;
  *checksums.txt)               exit 1 ;;
  */releases/download/*.tar.gz) cp "` + tarPath + `" "$out"; exit 0 ;;
esac
exit 1
`
	writeExecutable(t, filepath.Join(binDir, "curl"), curlShim)

	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PREFIX="+filepath.Join(tmp, "prefix"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	got := string(out)
	if strings.Contains(got, "could not resolve latest release") {
		t.Fatalf("version resolution regressed (API 403 not survived):\n%s", got)
	}
	if !strings.Contains(got, "installed latere "+wantVersion) {
		t.Fatalf("expected install of %s, got:\n%s", wantVersion, got)
	}
	if !strings.Contains(got, "latere "+wantVersion+" (test build)") {
		t.Fatalf("installed binary did not report its version:\n%s", got)
	}
}

func writeReleaseTarball(t *testing.T, path, binContent string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "latere",
		Mode:     0o755,
		Size:     int64(len(binContent)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(binContent)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
