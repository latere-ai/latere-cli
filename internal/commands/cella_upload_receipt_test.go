// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestCellaUploadReceipt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", filepath.Join(root, "token.json"))
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(root, "absent-auth.json"))
	if err := api.SaveToken("", api.Token{AccessToken: "synthetic-token"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, contents, receipt string
		valid                   bool
	}{
		{"complete", "content", `{"files":1,"bytes":7,"dest":"/workspace"}`, true},
		{"empty file", "", `{"files":1,"bytes":0,"dest":"/workspace"}`, true},
		{"missing file", "content", `{"files":0,"bytes":7,"dest":"/workspace"}`, false},
		{"extra file", "content", `{"files":2,"bytes":7,"dest":"/workspace"}`, false},
		{"short bytes", "content", `{"files":1,"bytes":6,"dest":"/workspace"}`, false},
		{"extra bytes", "content", `{"files":1,"bytes":8,"dest":"/workspace"}`, false},
		{"negative bytes", "content", `{"files":1,"bytes":-1,"dest":"/workspace"}`, false},
		{"missing counts", "content", `{}`, false},
		{"null", "content", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(source, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error(err)
				}
				_, _ = w.Write([]byte(tc.receipt))
			}))
			defer server.Close()
			cmd := newCeUploadCmd()
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"dev", source, "--api-url", server.URL})
			err := cmd.Execute()
			if tc.valid {
				if err != nil {
					t.Errorf("valid receipt rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "upload receipt") {
				t.Errorf("invalid receipt returned %v", err)
			}
		})
	}
}
