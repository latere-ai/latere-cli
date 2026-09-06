// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/commands"
)

func TestCellaConfiguredInputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	token, source := filepath.Join(root, "token.json"), filepath.Join(root, "source.tar")
	if err := os.WriteFile(token, []byte(`{"access_token":"synthetic-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 1024)
	if err := os.WriteFile(source, want, 0600); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, mode := range []string{"write", "import"} {
			for _, input := range []string{"default", "dash"} {
				t.Run(prefix+"/"+mode+"/"+input, func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Header.Get("Authorization") != "Bearer synthetic-token" {
							t.Error("incorrect authorization")
						}
						var got []byte
						if mode == "write" {
							if r.Method != http.MethodPut || r.URL.Path != "/v1/sandboxes/dev/files" {
								t.Errorf("request=%s %s", r.Method, r.URL)
							}
							var body struct{ Path, Content string }
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
							}
							if body.Path != "/workspace/file" {
								t.Errorf("path=%q", body.Path)
							}
							var err error
							got, err = base64.StdEncoding.DecodeString(body.Content)
							if err != nil {
								t.Error(err)
							}
							w.WriteHeader(http.StatusNoContent)
						} else {
							if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/dev/files/import" {
								t.Errorf("request=%s %s", r.Method, r.URL)
							}
							if err := r.ParseMultipartForm(1 << 20); err != nil {
								t.Error(err)
								w.WriteHeader(400)
								return
							}
							defer r.MultipartForm.RemoveAll()
							file, header, err := r.FormFile("tarball")
							if err != nil {
								t.Error(err)
								w.WriteHeader(400)
								return
							}
							defer file.Close()
							got, err = io.ReadAll(file)
							if err != nil {
								t.Error(err)
							}
							_ = json.NewEncoder(w).Encode(map[string]any{"imported": header.Filename, "bytes": len(got)})
						}
						if !bytes.Equal(got, want) {
							t.Errorf("wrong input uploaded: %q (%d bytes)", got, len(got))
						}
					}))
					defer server.Close()
					args := []string{"-test.run=^TestCellaConfiguredInputHelperProcess$", "--", prefix, mode, "dev"}
					if mode == "write" {
						args = append(args, "/workspace/file")
					}
					if input == "dash" {
						args = append(args, "--input", "-")
					}
					args = append(args, "--api-url", server.URL)
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					command.Stdin = strings.NewReader("unrelated process stdin")
					command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+token, "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "LATERE_TEST_CONFIGURED_INPUT="+source)
					if out, err := command.CombinedOutput(); err != nil {
						t.Errorf("command: %v %s", err, out)
					}
					if requests.Load() != 1 {
						t.Errorf("requests=%d", requests.Load())
					}
				})
			}
		}
	}
}

// Configure an inherited input separately from the process's stdin, then
// exercise the full command tree and normal error-to-exit handling.
func TestCellaConfiguredInputHelperProcess(t *testing.T) {
	source := os.Getenv("LATERE_TEST_CONFIGURED_INPUT")
	if source == "" {
		return
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	root := commands.NewRoot("test")
	root.SetIn(file)
	root.SetArgs(os.Args[3:])
	err = root.Execute()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Exit(commands.HandleExitError(os.Stderr, err))
	}
	os.Exit(0)
}
