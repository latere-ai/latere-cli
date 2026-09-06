// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"compress/gzip"
	"errors"
	"io"
	"testing"
)

func TestExtractBinaryGzipIntegrity(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
		want error
	}{
		{"valid", func(b []byte) []byte { return b }, nil},
		{"bad CRC", func(b []byte) []byte { b[len(b)-8] ^= 1; return b }, gzip.ErrChecksum},
		{"bad size", func(b []byte) []byte { b[len(b)-4] ^= 1; return b }, gzip.ErrChecksum},
		{"missing footer", func(b []byte) []byte { return b[:len(b)-8] }, io.ErrUnexpectedEOF},
		{"partial footer", func(b []byte) []byte { return b[:len(b)-1] }, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := tc.edit(makeTarGz(t, map[string]string{"latere": "BINARY-BYTES"}))
			got, err := extractBinary(archive)
			if tc.want == nil {
				if err != nil || string(got) != "BINARY-BYTES" {
					t.Fatalf("valid archive: binary=%q error=%v", got, err)
				}
			} else if !errors.Is(err, tc.want) || got != nil {
				t.Fatalf("corrupt archive: binary=%q error=%v, want %v", got, err, tc.want)
			}
		})
	}
}
