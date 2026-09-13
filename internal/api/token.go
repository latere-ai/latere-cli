// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"latere.ai/x/pkg/atomicfile"

	"github.com/latere-ai/latere-cli/internal/config"
)

// Token is the saved login: the issuer-addressed access token and the
// refresh token that renews it. The shape matches an OAuth2 token
// response so the device-code reply is written back unchanged.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	IssuedAt     time.Time `json:"issued_at"`
}

// AuthTokenPath returns the path to auth-token.json, the one credential
// the CLI keeps on disk. Every product credential is minted from it, so
// nothing else is stored beside it. Override with LATERE_AUTH_TOKEN_FILE
// for testing.
func AuthTokenPath() string {
	if v := os.Getenv("LATERE_AUTH_TOKEN_FILE"); v != "" {
		return v
	}
	return config.Path("auth-token.json")
}

// ErrNoToken means the file does not exist (the user hasn't logged in).
var ErrNoToken = errors.New("not logged in; run `latere login`")

// LoadAuthToken reads the saved login. Returns ErrNoToken when the file
// is absent, which is what a signed-out machine looks like.
func LoadAuthToken() (Token, error) {
	p := AuthTokenPath()
	if p == "" {
		return Token{}, ErrNoToken
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Token{}, ErrNoToken
		}
		return Token{}, err
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return Token{}, fmt.Errorf("parse token file: %w", err)
	}
	return t, nil
}

// SaveAuthToken atomically replaces auth-token.json with 0600 perms,
// syncing the write before publishing it. Creates the directory if
// missing. Permissions are best-effort on Windows.
func SaveAuthToken(t Token) error {
	p := AuthTokenPath()
	if p == "" {
		return errors.New("cannot determine token path")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteSync(p, b, 0o600)
}

// ClearAuthToken deletes auth-token.json. Idempotent.
func ClearAuthToken() error {
	p := AuthTokenPath()
	if p == "" {
		return nil
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
