// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpgradeCheckOutputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, latest := range []string{"v1.0.0", "v9.9.9"} {
		for _, writable := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/writable=%t", latest, writable), func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "output")
				if err := os.WriteFile(path, []byte("existing\n"), 0600); err != nil {
					t.Fatal(err)
				}
				mode := os.O_RDONLY
				if writable {
					mode = os.O_WRONLY | os.O_APPEND
				}
				out, err := os.OpenFile(path, mode, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = out.Close() }()
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodHead || r.URL.Path != "/latere-ai/latere-cli/releases/latest" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					w.Header().Set("Location", "/latere-ai/latere-cli/releases/tag/"+latest)
					w.WriteHeader(http.StatusFound)
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "-test.run=^TestUpgradeLatestStatusHelperProcess$")
				command.Env = append(os.Environ(), "LATERE_TEST_LATEST_SERVER="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
				var diagnostic bytes.Buffer
				command.Stdout, command.Stderr = out, &diagnostic
				runErr := command.Run()
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				want := "existing\n"
				if writable {
					if latest == "v1.0.0" {
						want += "latere v1.0.0 is already the latest release.\n"
					} else {
						want += "A new release of latere is available: v1.0.0 -> v9.9.9\nRun `latere upgrade` to update.\n"
					}
					if runErr != nil || diagnostic.Len() != 0 {
						t.Errorf("successful output: error=%v stderr=%q", runErr, diagnostic.String())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](runErr); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "write upgrade check result") {
					t.Errorf("failed output: error=%v stderr=%q", runErr, diagnostic.String())
				}
				if string(got) != want || requests.Load() != 1 {
					t.Errorf("output=%q want=%q requests=%d", got, want, requests.Load())
				}
			})
		}
	}
}
