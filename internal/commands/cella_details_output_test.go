// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestCellaDetailsConfiguredOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, dir, "synthetic-token"))
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	manifest := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifest, []byte("apiVersion: cella.latere.ai/v1\nkind: Sandbox\nspec: {image: test}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const detail = `{"id":"sb-test","name":"dev","state":"running","tier":"persistent","disk_gb":5,"cpu_milli":1000,"memory_mb":2048,"workdir":"/workspace"}`
	const record = "cella:      dev\nid:         sb-test\nstate:      running\ntier:       persistent\ndisk:       5Gi\nresources:  cpu=1000m memory=2048Mi\nworkdir:    /workspace\n"
	for _, tc := range []struct {
		name                     string
		args                     []string
		method, path, body, want string
	}{
		{"apply", []string{"apply", "-f", manifest}, "POST", "/v1/sandboxes", detail, record},
		{"rename", []string{"rename", "old", "dev"}, "PATCH", "/v1/sandboxes/old", detail, record},
		{"start", []string{"start", "dev"}, "POST", "/v1/sandboxes/dev/start", detail, record},
		{"stop", []string{"stop", "dev"}, "POST", "/v1/sandboxes/dev/stop", detail, record},
		{"extend", []string{"extend", "dev", "--hours", "1"}, "POST", "/v1/sandboxes/dev/extend", detail, record},
		{"convert", []string{"convert", "dev", "--to", "persistent"}, "POST", "/v1/sandboxes/dev/convert", detail, record},
		{"resize", []string{"resize", "dev", "--disk-gb", "10"}, "POST", "/v1/sandboxes/dev/resize", detail, record},
		{"list", []string{"list"}, "GET", "/v1/sandboxes", "[" + detail + "," + detail + "]", record + "\n" + record},
		{"empty list", []string{"list"}, "GET", "/v1/sandboxes", "[]", "No cellas are visible to this token.\n"},
	} {
		for _, failAfter := range []int{-1, 0, 3} {
			t.Run(fmt.Sprintf("%s/failAfter=%d", tc.name, failAfter), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != tc.method || r.URL.Path != tc.path {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				out := &evalOutputWriter{}
				sentinel := errors.New("cella output unavailable")
				if failAfter >= 0 {
					out.remaining, out.err = failAfter, sentinel
				}
				root := NewRoot("test")
				root.SetOut(out)
				root.SetErr(io.Discard)
				args := append([]string{"cella"}, tc.args...)
				root.SetArgs(append(args, "--api-url", server.URL))
				leaked, err := captureStdout(root.Execute)
				want := tc.want
				if failAfter >= 0 {
					want = want[:failAfter]
					if !errors.Is(err, sentinel) {
						t.Errorf("output error=%v", err)
					}
				} else if err != nil {
					t.Errorf("result output: %v", err)
				}
				if out.String() != want || leaked != "" {
					t.Errorf("configured=%q process stdout=%q, want configured=%q", out.String(), leaked, want)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}

func TestCellaListStopsAfterOutputFailure(t *testing.T) {
	const first = "cella:      first\nid:         sb-1\ntier:       -\n"
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			sentinel := errors.New("list output unavailable")
			out := &failingEnvWriter{failAt: failAt, err: sentinel}
			err := printSandboxList(out, []sandboxDTO{{ID: "sb-1", Name: "first"}, {ID: "sb-2", Name: "second"}})
			want := ""
			if failAt >= 2 {
				want = first
			}
			if failAt == 3 {
				want += "\n"
			}
			if !errors.Is(err, sentinel) || out.calls != failAt || out.String() != want {
				t.Errorf("error=%v writes=%d output=%q, want %q", err, out.calls, out.String(), want)
			}
		})
	}
}
