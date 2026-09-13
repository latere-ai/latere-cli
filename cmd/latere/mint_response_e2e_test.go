// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A product command presents the token the issuer minted for that
// product and nothing else: the login token goes to the issuer by POST,
// the actor token comes back, and Cella sees only the actor token. A
// reply that carries no lifetime is no token, and the command says so.
// A mint never rewrites the login on disk.
func TestMintPresentsOnlyTheMintedTokenE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, state := range []string{"complete", "whitespace", "no lifetime"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			authPath := filepath.Join(root, "auth-token.json")
			authBefore := []byte(`{"access_token":"auth-root"}`)
			if err := os.WriteFile(authPath, authBefore, 0600); err != nil {
				t.Fatal(err)
			}
			invalid := state != "complete" && state != "whitespace"
			var mints, requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/actor-tokens":
					mints.Add(1)
					if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer auth-root" {
						t.Errorf("the mint presented %q by %s, want the login token by POST", r.Header.Get("Authorization"), r.Method)
					}
					payload := `{"actor_token":"new-actor","expires_in":300}`
					switch state {
					case "whitespace":
						payload += " \r\n\t"
					case "no lifetime":
						payload = `{"actor_token":"new-actor"}`
					}
					_, _ = w.Write([]byte(payload))
				case "/v1/sandboxes":
					requests.Add(1)
					if got := r.Header.Get("Authorization"); got != "Bearer new-actor" {
						t.Errorf("Cella bearer = %q, want the minted actor token", got)
					}
					_, _ = w.Write([]byte(`[]`))
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "cella", "list", "--api-url", server.URL)
			command.Env = append(os.Environ(), "LATERE_CELLA_TOKEN=", "LATERE_AUTH_TOKEN_FILE="+authPath, "AUTH_URL="+server.URL, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			out, err := command.CombinedOutput()
			wantRequests := int32(1)
			if invalid {
				wantRequests = 0
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !bytes.Contains(out, []byte("latere login")) {
					t.Errorf("incomplete mint exit = %v: %s", err, out)
				}
			} else if err != nil {
				t.Errorf("complete mint failed: %v: %s", err, out)
			}
			if mints.Load() != 1 || requests.Load() != wantRequests {
				t.Errorf("mint/API calls = %d/%d; want 1/%d", mints.Load(), requests.Load(), wantRequests)
			}
			if data, err := os.ReadFile(authPath); err != nil || !bytes.Equal(data, authBefore) {
				t.Errorf("a mint rewrote the saved login: %v", err)
			}
		})
	}
}
