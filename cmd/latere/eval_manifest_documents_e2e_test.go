// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
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

	"gopkg.in/yaml.v3"
)

func TestEvalManifestDocumentsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, mode := range []string{"file", "stdin"} {
		for _, refs := range []bool{false, true} {
			first := "suite: first\n"
			if refs {
				first += "tasks:\n  - prompt: file://prompt.md\n"
			}
			for _, tc := range []struct {
				name, body string
				valid      bool
			}{
				{"single", first, true},
				{"explicit markers", "---\n" + first + "...\n# end\n", true},
				{"second suite", first + "---\nsuite: second\n", false},
				{"empty second", first + "---\n", false},
				{"invalid second", first + "---\nsuite: [\n", false},
				{"trailing garbage", first + "...\ngarbage\n", false},
			} {
				name := mode + "/" + tc.name
				if refs {
					name += " with prompt refs"
				}
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("prompt\n---\ntext\n"), 0600); err != nil {
						t.Fatal(err)
					}
					manifest := filepath.Join(dir, "suite.yaml")
					if err := os.WriteFile(manifest, []byte(tc.body), 0600); err != nil {
						t.Fatal(err)
					}
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if !tc.valid {
							t.Error("invalid manifest reached API")
						}
						var got struct {
							Suite string `yaml:"suite"`
							Tasks []struct {
								Text string `yaml:"prompt_text"`
							} `yaml:"tasks"`
						}
						if err := yaml.NewDecoder(r.Body).Decode(&got); err != nil {
							t.Error(err)
						}
						if got.Suite != "first" {
							t.Errorf("suite=%q", got.Suite)
						}
						if refs && (len(got.Tasks) != 1 || got.Tasks[0].Text != "prompt\n---\ntext\n") {
							t.Errorf("prompt changed: %+v", got.Tasks)
						}
						_, _ = io.WriteString(w, `{"dry_run":false,"suite":{"id":"st-1","name":"first","status":"created"}}`)
					}))
					defer server.Close()
					input := manifest
					if mode == "stdin" {
						input = "-"
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, "eval", "apply", "-f", input, "--api-url", server.URL)
					command.Dir = dir
					if mode == "stdin" {
						command.Stdin = strings.NewReader(tc.body)
					}
					command.Env = append(os.Environ(), "EVAL_ADMIN_TOKEN=synthetic-token", "LATERE_TOKEN_FILE="+filepath.Join(root, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
					var out, diagnostic bytes.Buffer
					command.Stdout, command.Stderr = &out, &diagnostic
					err := command.Run()
					if tc.valid {
						if err != nil || diagnostic.Len() != 0 || !strings.Contains(out.String(), "suite first") || requests.Load() != 1 {
							t.Errorf("valid manifest: err=%v output=%q stderr=%q requests=%d", err, out.String(), diagnostic.String(), requests.Load())
						}
					} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "manifest") || out.Len() != 0 || requests.Load() != 0 {
						t.Errorf("invalid manifest: err=%v output=%q stderr=%q requests=%d", err, out.String(), diagnostic.String(), requests.Load())
					}
				})
			}
		}
	}
}
