// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func jsonBenchToken() Token {
	return Token{
		AccessToken:  "eyJhbGciOiJSUzI1NiIsImtpZCI6ImxhdGVyZS0yMDI2LTEwIn0.eyJpc3MiOiJodHRwczovL2F1dGgubGF0ZXJlLmFpIiwic3ViIjoidXNyXzAxSkEzQlE3WjRNOE4yUDVSNlM3VDhWOVdYIiwiYXVkIjoibGF0ZXJlLWNsaSIsImV4cCI6MTc5MjAwMDAwMCwic2NvcGUiOiJvcGVuaWQgb2ZmbGluZV9hY2Nlc3MifQ.c2lnbmF0dXJlLXNpZ25hdHVyZS1zaWduYXR1cmUtc2lnbmF0dXJlLXNpZ25hdHVyZS1zaWduYXR1cmUtc2lnbmF0dXJlLXNpZ25hdHVyZQ",
		RefreshToken: "rt_01JA3BQ7Z4M8N2P5R6S7T8V9WXabcdefghijklmnopqrstuvwxyz0123456789",
		ClientID:     "latere-cli",
		TokenType:    "Bearer",
		ExpiresAt:    time.Date(2026, 10, 14, 10, 30, 0, 0, time.UTC),
		IssuedAt:     time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC),
	}
}

// BenchmarkJSONAuthTokenSaveMarshal is the encoding SaveAuthToken writes to
// auth-token.json on every login and refresh. The write itself syncs to disk
// and is left out.
func BenchmarkJSONAuthTokenSaveMarshal(b *testing.B) {
	t := jsonBenchToken()
	enc, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(enc)))
	for b.Loop() {
		if _, err := json.MarshalIndent(t, "", "  "); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJSONAuthTokenLoadUnmarshal is LoadAuthToken, which every command
// that talks to a product API runs to read the saved login.
func BenchmarkJSONAuthTokenLoadUnmarshal(b *testing.B) {
	enc, err := json.MarshalIndent(jsonBenchToken(), "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "auth-token.json")
	if err := os.WriteFile(path, enc, 0o600); err != nil {
		b.Fatal(err)
	}
	b.Setenv("LATERE_AUTH_TOKEN_FILE", path)
	b.ReportAllocs()
	b.SetBytes(int64(len(enc)))
	for b.Loop() {
		if _, err := LoadAuthToken(); err != nil {
			b.Fatal(err)
		}
	}
}
