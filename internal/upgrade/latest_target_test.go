// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLatestReleaseRedirectTarget(t *testing.T) {
	const release = "/latere-ai/latere-cli/releases/tag/"
	for _, tc := range []struct{ name, location, want string }{
		{"root relative", release + "v9.9.9", "v9.9.9"},
		{"relative", "tag/v9.9.9", "v9.9.9"},
		{"absolute", "@base" + release + "v9.9.9", "v9.9.9"},
		{"query", release + "v9.9.9?source=latest", "v9.9.9"},
		{"fragment", release + "v9.9.9#notes", "v9.9.9"},
		{"prerelease", release + "v9.9.9-rc1", "v9.9.9-rc1"},
		{"build", release + "v9.9.9+build.1", "v9.9.9+build.1"},
		{"wrong repo", "/other/repo/releases/tag/v9.9.9", ""},
		{"nested tag", release + "nested/v9.9.9", ""},
		{"query lookalike", "/login?return_to=" + release + "v9.9.9", ""},
		{"invalid version", release + "not-a-version", ""},
		{"external host", "https://example.invalid" + release + "v9.9.9", ""},
		{"encoded slash", release + "v9.9.9-rc%2F1", ""},
		{"encoded query", release + "v9.9.9-rc%3Ffoo", ""},
		{"encoded fragment", release + "v9.9.9-rc%23foo", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			var base string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodHead || r.URL.Path != "/"+repoSlug+"/releases/latest" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				w.Header().Set("Location", strings.ReplaceAll(tc.location, "@base", base))
				w.WriteHeader(http.StatusFound)
			}))
			defer server.Close()
			base = server.URL
			oldBase := githubBase
			githubBase = base
			defer func() { githubBase = oldBase }()
			tag, err := ResolveLatest(t.Context(), server.Client())
			if tc.want == "" {
				if err == nil || tag != "" {
					t.Errorf("invalid redirect returned tag=%q err=%v", tag, err)
				}
			} else if err != nil || tag != tc.want {
				t.Errorf("tag=%q err=%v, want %q", tag, err, tc.want)
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want 1", requests.Load())
			}
		})
	}
}
