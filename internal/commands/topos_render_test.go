// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"encoding/json"
	"testing"

	"latere.ai/x/topos/models/anthropic"
)

func TestSummarizeToolInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bash command", `{"command":"go build ./..."}`, " (go build ./...)"},
		{"read path", `{"file_path":"/a/b.go","offset":1}`, " (/a/b.go)"},
		{"grep pattern", `{"pattern":"TODO"}`, " (TODO)"},
		{"no salient key", `{"foo":"bar"}`, ""},
		{"empty", ``, ""},
		{"not an object", `"x"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeToolInput(json.RawMessage(tc.in)); got != tc.want {
				t.Fatalf("summarizeToolInput(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestModelString(t *testing.T) {
	m := anthropic.New("k", "", anthropic.WithModel("claude-test-9"))
	if got := modelString(m); got != "claude-test-9" {
		t.Fatalf("modelString = %q, want claude-test-9", got)
	}
}
