// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"net/http"
)

// discoveryPath is where an issuer publishes its endpoints, which the SDK's
// sign-in calls read before the call they make.
const discoveryPath = "/.well-known/openid-configuration"

// serveDiscovery answers the issuer's discovery document when r asks for
// it, naming the endpoints at r's host where auth serves them, and reports
// whether it did.
func serveDiscovery(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL.Path != discoveryPath {
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

// handleDiscovery serves the issuer's discovery document on mux.
func handleDiscovery(mux *http.ServeMux) {
	mux.HandleFunc("GET "+discoveryPath, func(w http.ResponseWriter, r *http.Request) { serveDiscovery(w, r) })
}
