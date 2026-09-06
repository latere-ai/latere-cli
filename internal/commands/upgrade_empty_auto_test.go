// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUpgradeEmptyAuto(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var requests atomic.Int32
	http.DefaultTransport = transferTimeoutTransport(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected release request blocked by test")
	})
	for _, auto := range [][]string{{"--auto="}, {"--auto", ""}} {
		for _, tc := range []struct {
			name, want string
			args       []string
		}{
			{"alone", `invalid value "" for --auto`, nil},
			{"check false", `invalid value "" for --auto`, []string{"--check=false"}},
			{"check", "--auto cannot be combined with --check", []string{"--check"}},
			{"version", "--auto cannot be combined with a version argument", []string{"v9.9.9"}},
		} {
			t.Run(strings.Join(auto, " ")+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", dir)
				config := filepath.Join(dir, "latere", "config.json")
				if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
					t.Fatal(err)
				}
				before := []byte(`{"auto_upgrade":false}`)
				if err := os.WriteFile(config, before, 0600); err != nil {
					t.Fatal(err)
				}
				requests.Store(0)
				root := NewRoot("v1.0.0")
				var out bytes.Buffer
				root.SetOut(&out)
				root.SetErr(io.Discard)
				root.SetArgs(append(append([]string{"upgrade"}, auto...), tc.args...))
				err := root.Execute()
				if err == nil || !strings.Contains(err.Error(), tc.want) || out.Len() != 0 || requests.Load() != 0 {
					t.Errorf("error=%v output=%q requests=%d", err, out.String(), requests.Load())
				}
				after, err := os.ReadFile(config)
				if err != nil || !bytes.Equal(after, before) {
					t.Errorf("config changed: %q error=%v", after, err)
				}
			})
		}
	}
}
