// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUpgradeEmptyTarget(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var requests atomic.Int32
	http.DefaultTransport = transferTimeoutTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.Method != http.MethodHead || r.URL.String() != "https://github.com/latere-ai/latere-cli/releases/latest" {
			return nil, fmt.Errorf("unexpected release request blocked by test: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://github.com/latere-ai/latere-cli/releases/tag/v1.0.0"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	for _, target := range [][]string{nil, {""}, {" "}, {"\t"}} {
		for _, check := range []bool{true, false} {
			t.Run(fmt.Sprintf("target=%q/check=%t", target, check), func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", dir)
				requests.Store(0)
				root := NewRoot("v1.0.0")
				var out bytes.Buffer
				root.SetOut(&out)
				root.SetErr(io.Discard)
				root.SetArgs(append([]string{"upgrade", fmt.Sprintf("--check=%t", check)}, target...))
				err := root.Execute()
				if target == nil {
					if err != nil || requests.Load() != 1 || out.String() != "latere v1.0.0 is already the latest release.\n" {
						t.Errorf("default upgrade: error=%v requests=%d output=%q", err, requests.Load(), out.String())
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), "invalid version") || requests.Load() != 0 || out.Len() != 0 {
					t.Errorf("empty target: error=%v requests=%d output=%q", err, requests.Load(), out.String())
				}
				if _, err := os.Stat(filepath.Join(dir, "latere", "update-check.json")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("update cache changed: %v", err)
				}
			})
		}
	}
}
