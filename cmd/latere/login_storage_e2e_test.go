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
	"runtime"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestLoginReplacesPermissiveTokenE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	path := filepath.Join(root, "token.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"old-test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sandboxes" || r.Header.Get("Authorization") != "Bearer new-test-token" {
			t.Error("unexpected login verification request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "login", "--token", "new-test-token", "--no-git", "--api-url", server.URL)
	command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+path, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "auth-token.json"), "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	got, err := api.LoadToken(path)
	if err != nil || got.AccessToken != "new-test-token" {
		t.Fatalf("login did not save the new token: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("login left token permissions %04o, want 0600", info.Mode().Perm())
	}
}
