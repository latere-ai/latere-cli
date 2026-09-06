// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"fmt"
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

func TestEvalResolvedPromptTextE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, mode := range []string{"file", "stdin"} {
		for _, tc := range []struct{ name, ref, field, want string }{
			{"resolved existing file", "existing.md", "prompt_text: pinned prompt", "pinned prompt"},
			{"resolved missing file", "missing.md", "prompt_text: pinned prompt", "pinned prompt"},
			{"unresolved file", "existing.md", `prompt_text: ""`, "changed local prompt\n"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				for name, content := range map[string]string{"existing.md": "changed local prompt\n", "fresh.md": "freshly resolved prompt\n"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				body := fmt.Sprintf("suite: test\ntasks:\n  - prompt: file://%s\n    %s\n  - prompt: file://fresh.md\n", tc.ref, tc.field)
				manifest := filepath.Join(dir, "suite.yaml")
				if err := os.WriteFile(manifest, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/api/v1/suites/apply" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					}
					var got struct {
						Tasks []struct {
							Prompt string `yaml:"prompt"`
							Text   string `yaml:"prompt_text"`
						} `yaml:"tasks"`
					}
					if err := yaml.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					if len(got.Tasks) != 2 {
						t.Errorf("tasks=%+v", got.Tasks)
					} else {
						if got.Tasks[0].Prompt != "file://"+tc.ref || got.Tasks[0].Text != tc.want {
							t.Errorf("first task=%+v, want text=%q", got.Tasks[0], tc.want)
						}
						if got.Tasks[1].Prompt != "file://fresh.md" || got.Tasks[1].Text != "freshly resolved prompt\n" {
							t.Errorf("second task=%+v", got.Tasks[1])
						}
					}
					_, _ = io.WriteString(w, `{"dry_run":false,"suite":{"id":"st-1","name":"test","status":"exists"}}`)
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
				if err := command.Run(); err != nil || diagnostic.Len() != 0 || !strings.Contains(out.String(), "suite test (exists)") {
					t.Errorf("apply: err=%v output=%q stderr=%q", err, out.String(), diagnostic.String())
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
