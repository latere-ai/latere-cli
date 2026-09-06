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

func TestGitCredentialRejectsProtocolControlBytesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, source := range []string{"saved auth", "pasted", "refreshed"} {
		for _, tc := range []struct {
			name, token string
			invalid     bool
		}{
			{"ordinary", "valid-token", false},
			{"padding", "key==", false},
			{"spaces", "key with spaces", false},
			{"LF", "key\nusername=injected\npassword=replaced", true},
			{"CR", "key\rpassword=replaced", true},
			{"NUL", "key\x00suffix", true},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
				cellaToken := "unrelated-cella"
				if source == "pasted" {
					cellaToken = tc.token
				}
				cellaBefore, _ := json.Marshal(map[string]string{"access_token": cellaToken})
				if err := os.WriteFile(cellaPath, cellaBefore, 0600); err != nil {
					t.Fatal(err)
				}
				// The value git receives is the minted Drive token; the root
				// token (saved or refreshed) is only the bearer of the mint.
				var authBefore []byte
				wantBearer := "Bearer saved-root"
				if source != "pasted" {
					value := map[string]any{"access_token": "saved-root"}
					if source == "refreshed" {
						value = map[string]any{"access_token": "old-root", "refresh_token": "test-refresh", "expires_at": time.Now().Add(-time.Hour)}
						wantBearer = "Bearer new-root"
					}
					authBefore, _ = json.Marshal(value)
					if err := os.WriteFile(authPath, authBefore, 0600); err != nil {
						t.Fatal(err)
					}
				}
				var refreshes, mints atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch {
					case r.Method == http.MethodPost && r.URL.Path == "/token":
						refreshes.Add(1)
						_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-root", "refresh_token": "new-refresh", "token_type": "Bearer", "expires_in": 3600})
					case r.Method == http.MethodPost && r.URL.Path == "/actor-tokens":
						mints.Add(1)
						if got := r.Header.Get("Authorization"); got != wantBearer {
							t.Errorf("mint bearer = %q, want %q", got, wantBearer)
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"actor_token": tc.token, "expires_in": 300})
					default:
						t.Errorf("unexpected auth request: %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "git-credential", "get", "--auth-url", server.URL)
				command.Stdin = strings.NewReader("protocol=https\nhost=drive.latere.ai\n\n")
				command.Env = append(os.Environ(), "DRIVE_HOST=drive.latere.ai", "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_CLIENT_ID=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				want := "username=token\npassword=" + tc.token + "\n\n"
				if tc.invalid {
					want = ""
				}
				if err != nil || stdout.String() != want || stderr.Len() != 0 {
					t.Errorf("helper=%v, stdout=%q stderr=%q, want stdout=%q", err, stdout.String(), stderr.String(), want)
				}
				var wantRefreshes, wantMints int32 = 0, 1
				if source == "refreshed" {
					wantRefreshes = 1
				}
				if source == "pasted" {
					wantMints = 0 // no root token to mint from; the paste is presented verbatim
				}
				if refreshes.Load() != wantRefreshes || mints.Load() != wantMints {
					t.Errorf("refresh requests=%d mint requests=%d, want %d and %d", refreshes.Load(), mints.Load(), wantRefreshes, wantMints)
				}
				if data, err := os.ReadFile(cellaPath); err != nil || !bytes.Equal(data, cellaBefore) {
					t.Errorf("helper changed saved Cella token: %v", err)
				}
				if source == "saved auth" {
					if data, err := os.ReadFile(authPath); err != nil || !bytes.Equal(data, authBefore) {
						t.Errorf("helper changed saved auth token: %v", err)
					}
				}
			})
		}
	}
}
