// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLuxServeRejectsIncompleteDiscoveryE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, runtime := range []string{"ollama", "openai-compat"} {
		for _, state := range []string{"short content length", "over limit"} {
			t.Run(runtime+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				payload := `{"models":[{"name":"test-model"}]}`
				if runtime == "openai-compat" {
					payload = `{"data":[{"id":"test-model"}]}`
				}
				if state == "over limit" {
					payload += strings.Repeat(" ", (1<<20)+1-len(payload))
				}
				var probes atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					probes.Add(1)
					if state == "short content length" {
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
					}
					_, _ = w.Write([]byte(payload))
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "lux", "serve", "--runtime", runtime, "--upstream", server.URL, "--lux-url", server.URL, "--auth-url", server.URL, "--share", "owner")
				command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+filepath.Join(root, "token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_LUX_TOKEN=", "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
				out, err := command.CombinedOutput()
				if ctx.Err() != nil || err == nil || !strings.Contains(string(out), "no "+runtime+" runtime is answering") || strings.Contains(string(out), "model(s)") || probes.Load() != 1 {
					t.Fatalf("invalid discovery did not stop preflight: err=%v, probes=%d, output=%q", err, probes.Load(), out)
				}
			})
		}
	}
}
