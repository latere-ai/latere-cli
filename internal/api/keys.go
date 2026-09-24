// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"latere.ai/x/pkg/otel"
)

// A key is created and revoked at auth's /me/keys (auth specs/084): a
// personal key in the personal context, a member key in an organization's.
// The CLI creates one for the model endpoints on first use
// (specs/006-model-key.md) and presents it as the bearer of every model
// call; auth announces it to platformd, which registers it at the Lux core.

// ModelGrant is the one grant a CLI model key carries: model.use on every
// Model. Which Models it reaches is the context's model list, and what it
// may spend is the context's wallet.
const ModelGrant = `[{"type":"latere-authz","actions":["lux:model.use"],"datatypes":["Model"],"locations":["https://api.latere.ai"]}]`

// KeyRequest is what the CLI asks auth for.
type KeyRequest struct {
	Name string
	// OrgID is the organization the key acts in, "" for the personal
	// context.
	OrgID     string
	ExpiresAt time.Time
}

// CreatedKey is auth's answer: the value exactly once, and the key's
// status in its context.
type CreatedKey struct {
	ID        string     `json:"id"`
	Prefix    string     `json:"prefix"`
	Key       string     `json:"key"`
	OrgID     *string    `json:"org_id"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// KeyError is auth refusing a key request: the HTTP status, auth's error
// code and its sentence for the person.
type KeyError struct {
	Status  int
	Code    string
	Message string
}

func (e *KeyError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Code)
	}
	return fmt.Sprintf("auth answered %d %s", e.Status, e.Code)
}

// keyHTTP is the client key requests are sent with.
var keyHTTP = &http.Client{Timeout: 30 * time.Second, Transport: otel.Transport(nil), CheckRedirect: PreserveMethodOnRedirect}

// CreateModelKey creates a key with ModelGrant at auth, with the login's
// access token. authBase is auth's base URL.
func CreateModelKey(ctx context.Context, authBase, access string, r KeyRequest) (CreatedKey, error) {
	body := map[string]any{
		"name":       r.Name,
		"grants":     json.RawMessage(ModelGrant),
		"expires_at": r.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if r.OrgID != "" {
		body["org_id"] = r.OrgID
	}
	b, err := json.Marshal(body)
	if err != nil {
		return CreatedKey{}, fmt.Errorf("encode key request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(authBase, "/")+"/me/keys", bytes.NewReader(b))
	if err != nil {
		return CreatedKey{}, fmt.Errorf("create key: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := keyHTTP.Do(req)
	if err != nil {
		return CreatedKey{}, fmt.Errorf("create key: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return CreatedKey{}, fmt.Errorf("create key: read the answer: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return CreatedKey{}, keyError(resp.StatusCode, raw)
	}
	var k CreatedKey
	if err := json.Unmarshal(raw, &k); err != nil {
		return CreatedKey{}, fmt.Errorf("create key: decode the answer: %w", err)
	}
	if k.ID == "" || k.Key == "" {
		return CreatedKey{}, errors.New("create key: auth answered no key")
	}
	return k, nil
}

// RevokeKey revokes one of the login's keys at auth. A key auth no longer
// knows is revoked already, so a 404 is not an error.
func RevokeKey(ctx context.Context, authBase, access, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		strings.TrimRight(authBase, "/")+"/me/keys/"+PathEscape(id), nil)
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := keyHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	return keyError(resp.StatusCode, raw)
}

// keyError reads auth's error envelope: {"error": code, "message": sentence}.
func keyError(status int, raw []byte) *KeyError {
	var env struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &env)
	return &KeyError{Status: status, Code: env.Error, Message: env.Message}
}
