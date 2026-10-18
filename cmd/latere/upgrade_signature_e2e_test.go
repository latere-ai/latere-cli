// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// upgradeTestKey makes a throwaway release key. It returns the environment
// entry that makes the upgrade helper process trust it, and a function that
// signs a checksums.txt body with it as tools/sign does. The release key's
// own private half is never used by a test.
func upgradeTestKey(t *testing.T) (env string, sign func(sums string) string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return "LATERE_TEST_UPGRADE_KEY=" + base64.StdEncoding.EncodeToString(public),
		func(sums string) string {
			return base64.StdEncoding.EncodeToString(ed25519.Sign(private, []byte(sums)))
		}
}

func TestUpgradeVerifiesTheSignatureE2E(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("self-replacement subprocess test")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	const replacement = "SIGNED-REPLACEMENT"
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "latere", Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(replacement))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, replacement); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	keyEnv, sign := upgradeTestKey(t)
	_, signWithOther := upgradeTestKey(t)
	const refusal = "this release is not signed by Latere; nothing was changed\n"
	for _, tc := range []struct {
		name, target string
		sig          func(sums string) string // "" serves no signature
		installed    bool
	}{
		{"good", "v9.9.9", sign, true},
		{"missing", "v9.9.9", func(string) string { return "" }, false},
		{"wrong key", "v9.9.9", signWithOther, false},
		{"truncated", "v9.9.9", func(sums string) string { return sign(sums)[:40] }, false},
		{"older than the first signed release", "v0.20.1", sign, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			disposable := filepath.Join(dir, "disposable-latere")
			if err := os.WriteFile(disposable, original, 0755); err != nil {
				t.Fatal(err)
			}
			version := strings.TrimPrefix(tc.target, "v")
			asset := fmt.Sprintf("latere_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
			sums := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive.Bytes()), asset)
			prefix := "/latere-ai/latere-cli/releases/download/" + tc.target + "/"
			var mu sync.Mutex
			var requested []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requested = append(requested, r.URL.Path)
				mu.Unlock()
				switch r.URL.Path {
				case prefix + asset:
					_, _ = w.Write(archive.Bytes())
				case prefix + "checksums.txt":
					_, _ = fmt.Fprint(w, sums)
				case prefix + "checksums.txt.sig":
					if body := tc.sig(sums); body != "" {
						_, _ = fmt.Fprint(w, body)
						return
					}
					http.NotFound(w, r)
				default:
					t.Errorf("unexpected release path=%s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, disposable, "-test.run=^TestUpgradeEmptyBinaryHelperProcess$", "--", "upgrade", tc.target)
			command.Env = append(os.Environ(), keyEnv, "LATERE_TEST_UPGRADE_SERVER="+server.URL, "LATERE_TEST_UPGRADE_COPY="+disposable, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
			var out, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &out, &diagnostic
			runErr := command.Run()
			installed, err := os.ReadFile(disposable)
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			paths := append([]string(nil), requested...)
			mu.Unlock()
			if tc.installed {
				if runErr != nil || diagnostic.Len() != 0 || out.String() != "Upgrading to latere v9.9.9...\nNow on latere v9.9.9.\n" {
					t.Errorf("error=%v stdout=%q stderr=%q", runErr, out.String(), diagnostic.String())
				}
				if string(installed) != replacement {
					t.Errorf("the signed release was not installed: size=%d", len(installed))
				}
				return
			}
			if exit, ok := errors.AsType[*exec.ExitError](runErr); !ok || exit.ExitCode() != 1 || diagnostic.String() != refusal {
				t.Errorf("error=%v stderr=%q want %q", runErr, diagnostic.String(), refusal)
			}
			if !bytes.Equal(installed, original) {
				t.Errorf("the executable changed: size=%d", len(installed))
			}
			for _, p := range paths {
				if p == prefix+asset {
					t.Errorf("the archive of a refused release was downloaded: %v", paths)
				}
			}
			if tc.target == "v0.20.1" && (len(paths) != 0 || out.Len() != 0) {
				t.Errorf("a release older than the first signed one was fetched: requests=%v stdout=%q", paths, out.String())
			}
			leftovers, err := filepath.Glob(filepath.Join(dir, ".latere-upgrade-*"))
			if err != nil || len(leftovers) != 0 {
				t.Errorf("upgrade leftovers=%v error=%v", leftovers, err)
			}
		})
	}
}
