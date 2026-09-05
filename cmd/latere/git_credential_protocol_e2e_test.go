// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestGitCredentialValidatesProtocolBeforeRefreshE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, deployment := range []struct {
		name, override, host string
		dev                  bool
	}{
		{"production", "", "drive.latere.ai", false},
		{"blank override", " \t ", "drive.latere.ai", false},
		{"development", "localhost:8080", "localhost:8080", true},
		{"padded override", " localhost:8080 ", "localhost:8080", true},
		{"different host", "localhost:8080", "drive.latere.ai", true},
	} {
		for _, protocol := range []string{"https", "http", "ftp", "ssh", "file", "", "missing"} {
			t.Run(deployment.name+"/"+protocol, func(t *testing.T) {
				root := t.TempDir()
				authPath := filepath.Join(root, "auth-token.json")
				before, _ := json.Marshal(map[string]any{"access_token": "old-root", "refresh_token": "test-refresh", "expires_at": time.Now().Add(-time.Hour)})
				if err := os.WriteFile(authPath, before, 0600); err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/token" {
						t.Errorf("unexpected auth request: %s %s", r.Method, r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"access_token":"new-root","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
				}))
				defer server.Close()
				input := "host=" + deployment.host + "\n\n"
				if protocol != "missing" {
					input = "protocol=" + protocol + "\n" + input
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "git-credential", "get", "--auth-url", server.URL)
				command.Stdin = strings.NewReader(input)
				command.Env = append(os.Environ(), "DRIVE_HOST="+deployment.override, "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_CLIENT_ID=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				allowed := deployment.name != "different host" && (protocol == "https" || protocol == "http" && deployment.dev)
				want := ""
				var wantCalls int32
				if allowed {
					want = "username=token\npassword=new-root\n\n"
					wantCalls = 1
				}
				if err != nil || stdout.String() != want || stderr.Len() != 0 {
					t.Errorf("credential result=%v, stdout=%q stderr=%q; want %q", err, stdout.String(), stderr.String(), want)
				}
				if calls.Load() != wantCalls {
					t.Errorf("refresh calls=%d, want %d", calls.Load(), wantCalls)
				}
				if !allowed {
					if data, err := os.ReadFile(authPath); err != nil || !bytes.Equal(data, before) {
						t.Errorf("rejected request modified auth credentials: %v", err)
					}
				}
			})
		}
	}
}
