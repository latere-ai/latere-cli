// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSavedLoginIsWrittenIndentedWithoutEmptyOptionalFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-token.json")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", path)
	at := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	if err := SaveAuthToken(Token{AccessToken: "a", ExpiresAt: at, IssuedAt: at}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"access_token\": \"a\",\n  \"expires_at\": \"2026-10-14T09:30:00Z\",\n  \"issued_at\": \"2026-10-14T09:30:00Z\"\n}"
	if string(got) != want {
		t.Errorf("auth-token.json = %q, want %q", got, want)
	}
}

func TestSavedLoginIsReadAsAPersonEditedIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-token.json")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", path)
	if err := os.WriteFile(path, []byte("{\"Access_Token\":\"first\",\"access_token\":\"second\",\"token_type\":\"B\xffr\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := LoadAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "second" || tok.TokenType != "B�r" {
		t.Errorf("token = %+v", tok)
	}
}
