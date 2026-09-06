// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDriveShareURLOutput(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, hasURL := range []bool{false, true} {
			for _, fail := range []bool{false, true} {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/api/v1/shares" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					}
					url := ""
					granteeType := "principal"
					if hasURL {
						url = "/s/synthetic-link"
						granteeType = "link"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "share-1", "status": "active", "permission": "read", "grantee_type": granteeType, "path_prefix": "files/item", "owner": "u-test", "existing": existing, "url": url})
				}))
				out := &failingEnvWriter{}
				sentinel := errors.New("stdout unavailable")
				if fail {
					out.failAt, out.err = 1, sentinel
				}
				var diagnostic bytes.Buffer
				cmd := newDriveCmd()
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetOut(out)
				cmd.SetErr(&diagnostic)
				args := []string{"--drive-url", server.URL, "--token", "synthetic-token", "share", "files/item"}
				if hasURL {
					args = append(args, "--link")
				} else {
					args = append(args, "--to", "u-test")
				}
				cmd.SetArgs(args)
				err := cmd.Execute()
				if fail && hasURL {
					if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "write share URL") || !strings.Contains(err.Error(), "share-1") {
						t.Errorf("existing=%t: error=%v, want wrapped URL output failure with share ID", existing, err)
					}
				} else if err != nil {
					t.Errorf("existing=%t hasURL=%t fail=%t: %v", existing, hasURL, fail, err)
				}
				want := ""
				if hasURL && !fail {
					want = server.URL + "/s/synthetic-link\n"
				}
				if out.String() != want {
					t.Errorf("output=%q, want %q", out.String(), want)
				}
				if !hasURL && out.calls != 0 {
					t.Errorf("person share wrote to stdout %d times", out.calls)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
				server.Close()
			}
		}
	}
}
