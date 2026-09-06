// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestApplyConfiguredManifestInput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, dir, "synthetic-token"))
	t.Setenv("EVAL_ADMIN_TOKEN", "synthetic-token")
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	process, err := os.CreateTemp(dir, "process-stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(process, "suite: wrong\n"); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = process
	t.Cleanup(func() { os.Stdin = previous; _ = process.Close() })
	for _, product := range []string{"cella", "sandbox", "eval"} {
		limit := 64 << 10
		if product == "eval" {
			limit = evalMaxManifestBytes
		}
		for _, mode := range []string{"stdin", "file", "empty", "read failure", "oversized"} {
			t.Run(product+"/"+mode, func(t *testing.T) {
				const manifest = "suite: intended\n"
				var input io.Reader = strings.NewReader(manifest)
				sentinel := errors.New("manifest source failed")
				wantError := ""
				switch mode {
				case "empty":
					input = strings.NewReader(" \n")
					wantError = "manifest is empty"
				case "read failure", "file":
					reader, writer := io.Pipe()
					_ = writer.CloseWithError(sentinel)
					defer reader.Close()
					input = io.MultiReader(strings.NewReader(manifest), reader)
					if mode == "read failure" {
						wantError = sentinel.Error()
					}
				case "oversized":
					input = strings.NewReader(manifest + strings.Repeat("#", limit))
					wantError = "byte limit"
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/yaml" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != manifest {
						t.Errorf("wrong manifest applied: %q %v", body, err)
					}
					if product == "eval" {
						if r.URL.Path != "/api/v1/suites/apply" {
							t.Errorf("path=%s", r.URL.Path)
						}
						_, _ = io.WriteString(w, `{"dry_run":false,"suite":{"id":"st-1","name":"intended","status":"exists"}}`)
					} else {
						if r.URL.Path != "/v1/sandboxes" {
							t.Errorf("path=%s", r.URL.Path)
						}
						_, _ = io.WriteString(w, `{"id":"sb-1","name":"intended"}`)
					}
				}))
				defer server.Close()
				path := "-"
				if mode == "file" {
					path = filepath.Join(t.TempDir(), "manifest.yaml")
					if err := os.WriteFile(path, []byte(manifest), 0600); err != nil {
						t.Fatal(err)
					}
				}
				root := NewRoot("test")
				root.SetIn(input)
				root.SetOut(io.Discard)
				root.SetErr(io.Discard)
				root.SetArgs([]string{product, "apply", "-f", path, "--api-url", server.URL})
				if _, err := process.Seek(0, io.SeekStart); err != nil {
					t.Fatal(err)
				}
				_, err := captureStdout(root.Execute)
				if wantError == "" {
					if err != nil || requests.Load() != 1 {
						t.Errorf("apply: %v requests=%d", err, requests.Load())
					}
				} else {
					if err == nil || !strings.Contains(err.Error(), wantError) || requests.Load() != 0 {
						t.Errorf("error=%v requests=%d, want %q before apply", err, requests.Load(), wantError)
					}
					if mode == "read failure" && !errors.Is(err, sentinel) {
						t.Errorf("input error identity lost: %v", err)
					}
				}
			})
		}
	}
}
