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

func TestEvalManifestLimitE2E(t *testing.T) {
	const overhead = len("suite: test\ntasks:\n    - prompt: file://prompt.md\n      prompt_text: \n")
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, mode := range []string{"file", "stdin"} {
		for _, tc := range []struct {
			name   string
			size   int
			prompt string
			refs   int
			valid  bool
		}{
			{name: "above Cella limit", size: (64 << 10) + 1, valid: true},
			{name: "exact Eval limit", size: 256 << 10, valid: true},
			{name: "over Eval limit", size: (256 << 10) + 1},
			{name: "large inline prompt", prompt: strings.Repeat("x", 100<<10), refs: 1, valid: true},
			{name: "exact resolved limit", prompt: strings.Repeat("x", (256<<10)-overhead), refs: 1, valid: true},
			{name: "over resolved limit", prompt: strings.Repeat("x", (256<<10)-overhead+1), refs: 1},
			{name: "oversized inline prompt", prompt: strings.Repeat("x", (256<<10)+1), refs: 1},
			{name: "combined prompts", prompt: strings.Repeat("x", 140<<10), refs: 2},
			{name: "YAML expansion", prompt: strings.Repeat("x\n", 50<<10), refs: 1},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				body := "suite: test\n# "
				if tc.size > 0 {
					body += strings.Repeat("x", tc.size-len(body))
				} else {
					body = "suite: test\ntasks:\n" + strings.Repeat("  - prompt: file://prompt.md\n", tc.refs)
					if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(tc.prompt), 0600); err != nil {
						t.Fatal(err)
					}
				}
				manifest := filepath.Join(dir, "suite.yaml")
				if err := os.WriteFile(manifest, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if !tc.valid {
						t.Error("oversized manifest reached API")
					}
					if len(data) > 256<<10 {
						t.Errorf("posted %d bytes", len(data))
					}
					if tc.size > 0 && string(data) != body {
						t.Error("manifest was truncated or changed")
					}
					if tc.refs > 0 {
						var got struct {
							Tasks []struct {
								Text string `yaml:"prompt_text"`
							} `yaml:"tasks"`
						}
						if err := yaml.Unmarshal(data, &got); err != nil {
							t.Error(err)
						}
						if len(got.Tasks) != tc.refs {
							t.Errorf("tasks=%d", len(got.Tasks))
						} else {
							for _, task := range got.Tasks {
								if task.Text != tc.prompt {
									t.Error("prompt was truncated or changed")
								}
							}
						}
					}
					_, _ = io.WriteString(w, `{"dry_run":false,"suite":{"id":"st-1","name":"test","status":"created"}}`)
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
					command.Stdin = strings.NewReader(body)
				}
				command.Env = append(os.Environ(), "EVAL_ADMIN_TOKEN=synthetic-token", "LATERE_TOKEN_FILE="+filepath.Join(root, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(root, "absent-auth.json"), "XDG_CONFIG_HOME="+root, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				if tc.valid {
					if err != nil || diagnostic.Len() != 0 || !strings.Contains(out.String(), "suite test (created)") || requests.Load() != 1 {
						t.Errorf("valid manifest: err=%v output=%q stderr=%q requests=%d", err, out.String(), diagnostic.String(), requests.Load())
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "262144") || !strings.Contains(diagnostic.String(), "limit") || requests.Load() != 0 || out.Len() != 0 {
					t.Errorf("oversized manifest: err=%v output=%q stderr=%q requests=%d", err, out.String(), diagnostic.String(), requests.Load())
				}
			})
		}
	}
}
