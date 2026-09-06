// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
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
	"sync/atomic"
	"testing"
	"time"
)

func TestUpgradeInstallOutputE2E(t *testing.T) {
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
	const replacement = "REPLACEMENT-BINARY"
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
	checksum := sha256.Sum256(archive.Bytes())
	asset := fmt.Sprintf("latere_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	for _, failure := range []string{"", "progress", "confirmation"} {
		t.Run("failure="+failure, func(t *testing.T) {
			dir := t.TempDir()
			disposable := filepath.Join(dir, "disposable-latere")
			if err := os.WriteFile(disposable, original, 0755); err != nil {
				t.Fatal(err)
			}
			var downloads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				if r.Method != http.MethodGet {
					t.Errorf("method=%s", r.Method)
				}
				switch r.URL.Path {
				case "/latere-ai/latere-cli/releases/download/v9.9.9/" + asset:
					_, _ = w.Write(archive.Bytes())
				case "/latere-ai/latere-cli/releases/download/v9.9.9/checksums.txt":
					_, _ = fmt.Fprintf(w, "%x  %s\n", checksum, asset)
				default:
					t.Errorf("unexpected release path=%s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, disposable, "-test.run=^TestUpgradeEmptyBinaryHelperProcess$", "--", "upgrade", "v9.9.9")
			command.Env = append(os.Environ(), "LATERE_TEST_UPGRADE_SERVER="+server.URL, "LATERE_TEST_UPGRADE_COPY="+disposable, "LATERE_TEST_UPGRADE_OUTPUT_FAILURE="+failure, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
			var out, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &out, &diagnostic
			err := command.Run()
			wantDownloads := int32(2)
			wantBinary := []byte(replacement)
			wantOut := "Upgrading to latere v9.9.9...\nNow on latere v9.9.9.\n"
			wantError := ""
			switch failure {
			case "progress":
				wantDownloads, wantBinary = 0, original
				wantOut = "Upg"
				wantError = "write upgrade progress"
			case "confirmation":
				wantOut = "Upgrading to latere v9.9.9...\nNow"
				wantError = "latere v9.9.9 was installed"
			}
			if wantError == "" {
				if err != nil || diagnostic.Len() != 0 {
					t.Errorf("successful output: error=%v stderr=%q", err, diagnostic.String())
				}
			} else {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
					t.Errorf("failed output: error=%v stderr=%q", err, diagnostic.String())
				}
				if !strings.Contains(diagnostic.String(), wantError) || !strings.Contains(diagnostic.String(), io.ErrClosedPipe.Error()) {
					t.Errorf("stderr=%q want=%q and write failure", diagnostic.String(), wantError)
				}
			}
			if out.String() != wantOut {
				t.Errorf("stdout=%q want=%q", out.String(), wantOut)
			}
			installed, err := os.ReadFile(disposable)
			if err != nil || !bytes.Equal(installed, wantBinary) {
				t.Errorf("executable contents: size=%d want=%d error=%v", len(installed), len(wantBinary), err)
			}
			if downloads.Load() != wantDownloads {
				t.Errorf("downloads=%d want=%d", downloads.Load(), wantDownloads)
			}
			leftovers, err := filepath.Glob(filepath.Join(dir, ".latere-upgrade-*"))
			if err != nil || len(leftovers) != 0 {
				t.Errorf("upgrade leftovers=%v error=%v", leftovers, err)
			}
		})
	}
}

type upgradeOutputFailureWriter struct {
	io.Writer
	successfulWrites int
}

func (w *upgradeOutputFailureWriter) Write(p []byte) (int, error) {
	if w.successfulWrites > 0 {
		w.successfulWrites--
		return w.Writer.Write(p)
	}
	n, err := w.Writer.Write(p[:min(3, len(p))])
	if err != nil {
		return n, err
	}
	return n, io.ErrClosedPipe
}
