// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// Match oauth2's token response limit, but require real EOF: its LimitReader
// otherwise treats a valid prefix at the limit as a complete response.
const authRefreshResponseLimit = 1 << 20

type authRefreshTransport struct {
	base http.RoundTripper
}

func (t authRefreshTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, authRefreshResponseLimit+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read auth refresh response: %w", err)
	}
	if len(data) > authRefreshResponseLimit {
		return nil, fmt.Errorf("auth refresh response exceeds %d bytes", authRefreshResponseLimit)
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, nil
}
