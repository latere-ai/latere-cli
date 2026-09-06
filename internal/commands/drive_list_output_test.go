// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDriveListOutputFailures(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	for _, long := range []bool{false, true} {
		t.Run(fmt.Sprintf("long=%t", long), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/files/me/files" || !r.URL.Query().Has("list") || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				_, _ = io.WriteString(w, `{"entries":[{"path":"files/one","size":1},{"path":"files/two","size":2}]}`)
			}))
			defer server.Close()
			execute := func(out io.Writer) error {
				root := NewRoot("test")
				root.SetOut(out)
				root.SetErr(io.Discard)
				args := []string{"drive", "ls", "--drive-url", server.URL, "--token", "synthetic-token"}
				if long {
					args = append(args, "--long")
				}
				root.SetArgs(args)
				return root.Execute()
			}
			baseline := &failingEnvWriter{}
			if err := execute(baseline); err != nil {
				t.Fatal(err)
			}
			want := "files/one\nfiles/two\n"
			if long {
				want = "1      files/one\n2      files/two\n"
			}
			if baseline.String() != want {
				t.Fatalf("listing=%q, want %q", baseline.String(), want)
			}
			for failAt := 1; failAt <= baseline.calls; failAt++ {
				t.Run(fmt.Sprint(failAt), func(t *testing.T) {
					sentinel := errors.New("file listing output unavailable")
					out := &failingEnvWriter{failAt: failAt, err: sentinel}
					err := execute(out)
					if !errors.Is(err, sentinel) || out.calls != failAt {
						t.Errorf("error=%v writes=%d, want failure on write %d", err, out.calls, failAt)
					}
					if !strings.HasPrefix(want, out.String()) {
						t.Errorf("listing continued after error: %q", out.String())
					}
				})
			}
			if want := int32(baseline.calls + 1); requests.Load() != want {
				t.Errorf("requests=%d, want %d", requests.Load(), want)
			}
		})
	}
}
