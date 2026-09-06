// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func TestExtractBinaryExpandedArchiveLimit(t *testing.T) {
	for _, tc := range []struct {
		name           string
		before, after  int
		limit          int64
		wantLimitError bool
	}{
		{"below limit", 1, 1, 4097, false},
		{"exact limit", 1, 1, 4096, false},
		{"one byte over", 1, 1, 4095, true},
		{"oversized preceding entry", 4096, 0, 4096, true},
		{"oversized following entry", 0, 4096, 4096, true},
		{"combined entries over limit", 1024, 1024, 4096, true},
		{"limit inside header", 1, 1, 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw bytes.Buffer
			tw := tar.NewWriter(&raw)
			for _, entry := range []struct{ name, data string }{
				{"README", strings.Repeat("a", tc.before)},
				{"latere", "BINARY"},
				{"LICENSE", strings.Repeat("z", tc.after)},
			} {
				if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0644, Size: int64(len(entry.data))}); err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(tw, entry.data); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			if _, err := gz.Write(raw.Bytes()); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			got, err := extractBinaryWithLimits(archive.Bytes(), 16, tc.limit)
			if tc.wantLimitError {
				if err == nil || !strings.Contains(err.Error(), "expanded archive exceeds") {
					t.Errorf("expanded size=%d limit=%d error=%v", raw.Len(), tc.limit, err)
				}
				if got != nil {
					t.Errorf("oversized archive returned binary=%q", got)
				}
			} else if err != nil || string(got) != "BINARY" {
				t.Errorf("valid archive: binary=%q error=%v", got, err)
			}
		})
	}
}
