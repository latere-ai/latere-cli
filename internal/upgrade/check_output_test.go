// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type checkOutputWriter struct {
	buffer bytes.Buffer
	limit  int
}

func (w *checkOutputWriter) Write(p []byte) (int, error) {
	if w.limit < 0 {
		return w.buffer.Write(p)
	}
	n, _ := w.buffer.Write(p[:min(len(p), w.limit)])
	return n, io.ErrClosedPipe
}

func TestUpgradeCheckOutput(t *testing.T) {
	for _, tc := range []struct {
		name, latest, want string
		check              bool
	}{
		{"available", "v9.9.9", "A new release of latere is available: v1.0.0 -> v9.9.9\nRun `latere upgrade` to update.\n", true},
		{"current", "v1.0.0", "latere v1.0.0 is already the latest release.\n", true},
		{"current without check", "v1.0.0", "latere v1.0.0 is already the latest release.\n", false},
	} {
		for _, limit := range []int{-1, 0, 3} {
			t.Run(fmt.Sprintf("%s/write=%d", tc.name, limit), func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodHead || r.URL.Path != "/"+repoSlug+"/releases/latest" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					w.Header().Set("Location", "/"+repoSlug+"/releases/tag/"+tc.latest)
					w.WriteHeader(http.StatusFound)
				}))
				defer server.Close()
				oldBase := githubBase
				githubBase = server.URL
				defer func() { githubBase = oldBase }()
				out := &checkOutputWriter{limit: limit}
				err := Run(t.Context(), "v1.0.0", "", tc.check, out)
				want := tc.want
				if limit < 0 {
					if err != nil {
						t.Errorf("successful output: %v", err)
					}
				} else {
					want = want[:limit]
					if !errors.Is(err, io.ErrClosedPipe) {
						t.Errorf("write error=%v", err)
					}
				}
				if out.buffer.String() != want || requests.Load() != 1 {
					t.Errorf("output=%q want=%q requests=%d", out.buffer.String(), want, requests.Load())
				}
			})
		}
	}
}
