// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/base64"
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

func TestWhoamiReportsOutputFailureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, fallback := range []bool{false, true} {
		for _, prefix := range []string{"", "auth"} {
			for _, mode := range []string{"writable", "read-only"} {
				t.Run(fmt.Sprintf("fallback=%v/%s/%s", fallback, prefix, mode), func(t *testing.T) {
					root := t.TempDir()
					tokenPath, outputPath := filepath.Join(root, "token.json"), filepath.Join(root, "output")
					token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"owner","principal_type":"user"}`)) + ".signature"
					tokenData := `{"access_token":"` + token + `"}`
					var probes, verifications atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/tokeninfo":
							probes.Add(1)
							if fallback {
								w.WriteHeader(http.StatusUnauthorized)
								return
							}
							_, _ = w.Write([]byte(`{"sub":"owner","principal_type":"user"}`))
						case "/v1/sandboxes":
							verifications.Add(1)
							_, _ = w.Write([]byte(`[]`))
						default:
							t.Errorf("unexpected request: %s", r.URL.Path)
							w.WriteHeader(http.StatusNotFound)
						}
					}))
					defer server.Close()
					if err := os.WriteFile(tokenPath, []byte(tokenData), 0o600); err != nil {
						t.Fatal(err)
					}
					const previous = "existing output\n"
					if err := os.WriteFile(outputPath, []byte(previous), 0o600); err != nil {
						t.Fatal(err)
					}
					flags := os.O_RDONLY
					if mode == "writable" {
						flags = os.O_WRONLY | os.O_APPEND
					}
					file, err := os.OpenFile(outputPath, flags, 0o600)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = file.Close() }()
					args := []string{"whoami"}
					if prefix != "" {
						args = append([]string{prefix}, args...)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+tokenPath, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"),
						"AUTH_URL="+server.URL, "SANDBOX_API_URL="+server.URL, "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
					var diagnostic bytes.Buffer
					command.Stdout, command.Stderr = file, &diagnostic
					err = command.Run()
					want := previous
					if mode == "writable" {
						want += "sub:           owner\nprincipal:     user\ncontext:       personal\n"
						if err != nil || diagnostic.Len() != 0 {
							t.Errorf("valid output failed: %v: %s", err, diagnostic.String())
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "write") {
						t.Errorf("failed output returned %v: %s", err, diagnostic.String())
					}
					if strings.Contains(diagnostic.String(), token) {
						t.Error("write diagnostic exposed token contents")
					}
					if got, err := os.ReadFile(outputPath); err != nil || string(got) != want {
						t.Errorf("output contents = %q (%v), want %q", got, err, want)
					}
					wantVerifications := int32(0)
					if fallback {
						wantVerifications = 1
					}
					if probes.Load() != 1 || verifications.Load() != wantVerifications {
						t.Errorf("auth/Cella requests=%d/%d, want 1/%d", probes.Load(), verifications.Load(), wantVerifications)
					}
					if got, err := os.ReadFile(tokenPath); err != nil || string(got) != tokenData {
						t.Errorf("printing changed the saved token: %v", err)
					}
				})
			}
		}
	}
}
