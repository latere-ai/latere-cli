// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

type transferTimeoutTransport func(*http.Request) (*http.Response, error)

func (f transferTimeoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestCellaTransferTimeout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	if err := api.SaveToken("", api.Token{AccessToken: "synthetic-token"}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "file")
	if err := os.WriteFile(source, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, verb := range []string{"upload", "import"} {
		for _, duration := range []string{"0", "5s", "-1s"} {
			t.Run(verb+"/"+duration, func(t *testing.T) {
				calls := 0
				http.DefaultTransport = transferTimeoutTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					deadline, present := r.Context().Deadline()
					if duration == "0" && present {
						t.Errorf("disabled timeout retained a deadline %s away", time.Until(deadline))
					}
					if duration == "5s" && (!present || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second) {
						t.Errorf("positive timeout deadline = %v, present=%v", deadline, present)
					}
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						return nil, err
					}
					_ = r.Body.Close()
					payloadBytes := 7
					if verb == "import" {
						payloadBytes = 2048
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"files":1,"bytes":%d,"dest":"/workspace","imported":"file"}`, payloadBytes))), Request: r}, nil
				})
				cmd := newCeUploadCmd()
				args := []string{"dev", source}
				if verb == "import" {
					cmd, args = newCeImportCmd(), []string{"dev", "--input", source}
				}
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(append(args, "--api-url", "http://upload.invalid", "--timeout", duration))
				err := cmd.Execute()
				if duration == "-1s" {
					if err == nil || !strings.Contains(err.Error(), "--timeout must not be negative") || calls != 0 {
						t.Errorf("negative timeout = %v, requests=%d", err, calls)
					}
				} else if err != nil || calls != 1 {
					t.Errorf("valid timeout = %v, requests=%d", err, calls)
				}
			})
		}
	}
}
