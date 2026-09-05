// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOrgContextDoesNotHideAuthFileErrorsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := filepath.Join(t.TempDir(), "latere")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	credential := func(org string) []byte {
		claims, _ := json.Marshal(map[string]string{"sub": "test-user", "org_id": org})
		token := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".test-signature"
		contents, _ := json.Marshal(map[string]string{"access_token": token})
		return contents
	}
	for _, tc := range []struct {
		name, want, wantError string
		contents              []byte
		directory             bool
	}{
		{name: "auth organization", contents: credential("auth-org"), want: "auth-org"},
		{name: "auth personal", contents: credential(""), want: "personal"},
		{name: "missing auth", want: "legacy-org"},
		{name: "malformed JSON", contents: []byte(`{"access_token":`), wantError: "parse token file"},
		{name: "wrong token type", contents: []byte(`{"access_token":42}`), wantError: "parse token file"},
		{name: "unreadable directory", directory: true, wantError: "auth-token.json"},
		{name: "empty auth", contents: []byte(`{}`), wantError: "saved token is not a JWT"},
		{name: "null auth", contents: []byte(`null`), wantError: "saved token is not a JWT"},
		{name: "invalid JWT", contents: []byte(`{"access_token":"invalid"}`), wantError: "saved token is not a JWT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cellaPath, authPath := filepath.Join(root, "token.json"), filepath.Join(root, "auth-token.json")
			cellaBefore := credential("legacy-org")
			if err := os.WriteFile(cellaPath, cellaBefore, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.directory {
				if err := os.Mkdir(authPath, 0700); err != nil {
					t.Fatal(err)
				}
			} else if tc.contents != nil {
				if err := os.WriteFile(authPath, tc.contents, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "org")
			command.Env = append(os.Environ(), "LATERE_TOKEN_FILE="+cellaPath, "LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if tc.wantError != "" {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), tc.wantError) {
					t.Errorf("auth failure hidden: err=%v, stdout=%q, stderr=%q", err, stdout.String(), stderr.String())
				}
			} else if err != nil || stdout.String() != tc.want+"\n" || stderr.Len() != 0 {
				t.Errorf("org context: err=%v, stdout=%q, stderr=%q, want %q", err, stdout.String(), stderr.String(), tc.want)
			}
			if contents, err := os.ReadFile(cellaPath); err != nil || !bytes.Equal(contents, cellaBefore) {
				t.Error("displaying context changed the Cella credential")
			}
			if tc.contents != nil {
				if contents, err := os.ReadFile(authPath); err != nil || !bytes.Equal(contents, tc.contents) {
					t.Error("displaying context changed the auth credential")
				}
			} else if tc.directory {
				if info, err := os.Stat(authPath); err != nil || !info.IsDir() {
					t.Error("displaying context changed the auth directory")
				}
			} else if _, err := os.Stat(authPath); !errors.Is(err, os.ErrNotExist) {
				t.Error("displaying context created an auth credential")
			}
		})
	}
}
