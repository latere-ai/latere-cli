// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestWhoamiRequiresIdentifiedPrincipalE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	// whoami reads the saved token and asks the issuer nothing: a token that
	// names its subject is printed, one that names nobody is an error, and
	// in neither case does a request reach the issuer.
	for _, saved := range []struct {
		name, payload string
		valid         bool
	}{
		{"identified", `{"sub":"saved-owner","principal_type":"user","org_id":"saved-org"}`, true},
		{"missing subject", `{"org_id":"saved-org"}`, false},
		{"empty subject", `{"sub":"","org_id":"saved-org"}`, false},
	} {
		t.Run(saved.name, func(t *testing.T) {
			root := t.TempDir()
			authPath := filepath.Join(root, "auth-token.json")
			t.Setenv("LATERE_AUTH_TOKEN_FILE", authPath)
			token := "header." + base64.RawURLEncoding.EncodeToString([]byte(saved.payload)) + ".signature"
			if err := api.SaveAuthToken(api.Token{AccessToken: token}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(authPath)
			if err != nil {
				t.Fatal(err)
			}
			var probes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probes.Add(1)
				t.Errorf("whoami called the issuer: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "whoami")
			command.Env = append(os.Environ(), "LATERE_AUTH_TOKEN_FILE="+authPath,
				"AUTH_URL="+server.URL, "XDG_CONFIG_HOME="+root,
				"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true")
			var out, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &out, &diagnostic
			err = command.Run()
			if saved.valid {
				if err != nil || !strings.Contains(out.String(), "saved-owner") || !strings.Contains(out.String(), "saved-org") {
					t.Errorf("identity missing: %v: %s %s", err, out.String(), diagnostic.String())
				}
			} else if err == nil || !strings.Contains(diagnostic.String(), "sub") {
				t.Errorf("a token naming nobody must fail naming sub: %v: %s %s", err, out.String(), diagnostic.String())
			}
			if probes.Load() != 0 {
				t.Errorf("issuer requests=%d, want none", probes.Load())
			}
			if data, err := os.ReadFile(authPath); err != nil || string(data) != string(before) {
				t.Errorf("whoami changed the saved login: %v", err)
			}
		})
	}
}
