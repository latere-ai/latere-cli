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

func TestCellaImportReceipt(t *testing.T) {
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
		{"complete", "content", `{"imported":"file","bytes":2048,"dest":"/workspace"}`, true},
		{"empty file", "", `{"imported":"file","bytes":1536,"dest":"/workspace"}`, true},
		{"missing name", "content", `{"bytes":2048,"dest":"/workspace"}`, false},
		{"wrong name", "content", `{"imported":"other","bytes":2048,"dest":"/workspace"}`, false},
		{"short bytes", "content", `{"imported":"file","bytes":2047,"dest":"/workspace"}`, false},
		{"extra bytes", "content", `{"imported":"file","bytes":2049,"dest":"/workspace"}`, false},
		{"negative bytes", "content", `{"imported":"file","bytes":-1,"dest":"/workspace"}`, false},
		{"missing bytes", "content", `{"imported":"file","dest":"/workspace"}`, false},
		{"null bytes", "content", `{"imported":"file","bytes":null,"dest":"/workspace"}`, false},
		{"missing receipt", "content", `{}`, false},
		{"null receipt", "content", `null`, false},
		{"no content", "content", "", false},
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
				if tc.receipt == "" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				_, _ = w.Write([]byte(tc.receipt))
			}))
			defer server.Close()
			cmd := newCeImportCmd()
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"dev", "--input", source, "--api-url", server.URL})
			err := cmd.Execute()
			if tc.valid {
				if err != nil {
					t.Errorf("valid receipt rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "import receipt") {
				t.Errorf("invalid receipt returned %v", err)
			}
		})
	}
}
