// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLatestReleaseHTTPStatus(t *testing.T) {
	for _, status := range []int{200, 201, 204, 301, 302, 303, 304, 307, 308, 404, 429, 500} {
		for _, current := range []string{"v1.0.0", "v9.9.9"} {
			t.Run(fmt.Sprintf("%d/%s", status, current), func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				oldState := checkState{LatestVersion: "v1.1.0", CheckedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
				if err := saveState(oldState); err != nil {
					t.Fatal(err)
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodHead || r.URL.Path != "/"+repoSlug+"/releases/latest" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					w.Header().Set("Location", "/"+repoSlug+"/releases/tag/v9.9.9")
					w.WriteHeader(status)
				}))
				defer server.Close()
				oldBase := githubBase
				githubBase = server.URL
				defer func() { githubBase = oldBase }()
				var out bytes.Buffer
				err := Run(t.Context(), current, "", true, &out)
				valid := status == 301 || status == 302 || status == 303 || status == 307 || status == 308
				if valid {
					if err != nil || !strings.Contains(out.String(), "v9.9.9") || loadState().LatestVersion != "v9.9.9" {
						t.Errorf("valid redirect: error=%v output=%q cache=%+v", err, out.String(), loadState())
					}
				} else if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("status %d", status)) || out.Len() != 0 || loadState() != oldState {
					t.Errorf("invalid status: error=%v output=%q cache=%+v", err, out.String(), loadState())
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}
