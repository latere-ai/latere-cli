// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"net/http"
)

// serveDiscovery answers the issuer's discovery document, which the SDK's
// sign-in calls read before the call they make, when r asks for it. It
// names the endpoints at r's host where auth serves them, and reports
// whether it answered.
func serveDiscovery(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL.Path != "/.well-known/openid-configuration" {
		return false
	}
	base := "http://" + r.Host
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"issuer":                        base,
		"authorization_endpoint":        base + "/authorize",
		"token_endpoint":                base + "/token",
		"userinfo_endpoint":             base + "/userinfo",
		"device_authorization_endpoint": base + "/device/code",
		"revocation_endpoint":           base + "/revoke",
	})
	return true
}
