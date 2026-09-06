// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"strings"
	"testing"
)

func TestExtractBinarySize(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    int
		wantErr string
	}{
		{"empty", 0, "empty latere binary"},
		{"below limit", 15, ""},
		{"exact limit", 16, ""},
		{"over limit", 17, "latere binary is too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := strings.Repeat("x", tc.size)
			archive := makeTarGz(t, map[string]string{"release/latere": payload})
			got, err := extractBinaryWithLimit(archive, 16)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || got != nil {
					t.Errorf("binary=%q error=%v, want %q", got, err, tc.wantErr)
				}
			} else if err != nil || string(got) != payload {
				t.Errorf("binary=%q error=%v", got, err)
			}
		})
	}
}
