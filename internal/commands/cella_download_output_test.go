// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCellaDownloadConfiguredOutput(t *testing.T) {
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), "synthetic-token"))
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	const body = "complete\x00file contents\n"
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, mode := range []string{"cat", "export", "export dash", "export file"} {
			for _, failAfter := range []int{-1, 0, 3} {
				t.Run(fmt.Sprintf("%s/%s/failAfter=%d", prefix, mode, failAfter), func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if mode == "cat" {
							if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/files") || r.URL.Query().Get("raw") != "true" {
								t.Errorf("request=%s %s", r.Method, r.URL)
							}
						} else if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/files/export") {
							t.Errorf("request=%s %s", r.Method, r.URL)
						}
						_, _ = io.WriteString(w, body)
					}))
					defer server.Close()
					out := &evalOutputWriter{}
					sentinel := errors.New("download output unavailable")
					if failAfter >= 0 {
						out.remaining, out.err = failAfter, sentinel
					}
					root := NewRoot("test")
					root.SetOut(out)
					root.SetErr(io.Discard)
					dest := filepath.Join(t.TempDir(), "archive.tar")
					args := []string{prefix, "export", "dev", "--api-url", server.URL}
					switch mode {
					case "cat":
						args = []string{prefix, "cat", "dev", "/workspace/file", "--api-url", server.URL}
					case "export dash":
						args = append(args, "-o", "-")
					case "export file":
						args = append(args, "-o", dest)
					}
					root.SetArgs(args)
					leaked, err := captureStdout(root.Execute)
					want := body
					if mode == "export file" {
						want = ""
						data, readErr := os.ReadFile(dest)
						if readErr != nil || string(data) != body {
							t.Errorf("file=%q error=%v", data, readErr)
						}
					} else if failAfter >= 0 {
						want = body[:failAfter]
					}
					if failAfter >= 0 && mode != "export file" {
						if !errors.Is(err, sentinel) {
							t.Errorf("lost output error: %v", err)
						}
					} else if err != nil {
						t.Errorf("download: %v", err)
					}
					if out.String() != want || leaked != "" {
						t.Errorf("configured=%q process stdout=%q; want configured=%q", out.String(), leaked, want)
					}
					if requests.Load() != 1 {
						t.Errorf("requests=%d", requests.Load())
					}
				})
			}
		}
	}
}
