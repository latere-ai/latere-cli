// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import "testing"

func TestDeviceFlowEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name, authEnv, apiEnv, authFlag, apiFlag, wantAuth, wantAPI string
	}{
		{name: "defaults", wantAuth: "https://auth.latere.ai", wantAPI: "https://cella.latere.ai"},
		{name: "infer_from_environment", apiEnv: "https://cella.example.test/", wantAuth: "https://auth.example.test", wantAPI: "https://cella.example.test"},
		{name: "environment", authEnv: "https://identity.example.test/", apiEnv: "https://compute.example.test/", wantAuth: "https://identity.example.test", wantAPI: "https://compute.example.test"},
		{name: "flags", authEnv: "https://wrong-auth.test", apiEnv: "https://wrong-api.test", authFlag: "https://identity.example.test/", apiFlag: "https://compute.example.test/", wantAuth: "https://identity.example.test", wantAPI: "https://compute.example.test"},
		{name: "infer_from_flag", apiEnv: "https://wrong-api.test", apiFlag: "https://cella.example.test/", wantAuth: "https://auth.example.test", wantAPI: "https://cella.example.test"},
		{name: "auth_environment_with_api_flag", authEnv: "https://identity.example.test/", apiFlag: "https://compute.example.test/", wantAuth: "https://identity.example.test", wantAPI: "https://compute.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AUTH_URL", tc.authEnv)
			t.Setenv("SANDBOX_API_URL", tc.apiEnv)
			auth, cella := (deviceFlowOpts{AuthURL: tc.authFlag, APIURL: tc.apiFlag}).endpoints()
			if auth != tc.wantAuth || cella != tc.wantAPI {
				t.Fatalf("endpoints = %q, %q; want %q, %q", auth, cella, tc.wantAuth, tc.wantAPI)
			}
		})
	}
}
