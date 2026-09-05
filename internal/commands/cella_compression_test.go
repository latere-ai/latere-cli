// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyImportTar(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "import", "payload.tar"))
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"tar", "tar.gz", "tar.bz2", "tar.xz"} {
		t.Run(suffix, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "import", "payload."+suffix))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := copyImportTar(&out, bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Fatal("decoded tar differs from original")
			}
			if suffix != "tar" {
				if err := copyImportTar(io.Discard, bytes.NewReader(raw[:len(raw)-1])); err == nil {
					t.Error("truncated compressed stream accepted")
				}
			}
		})
	}
}

type importErrorReader struct{ err error }

func (r importErrorReader) Read([]byte) (int, error) { return 0, r.err }

type importErrorWriter struct{ err error }

func (w importErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestCopyImportTarErrors(t *testing.T) {
	failure := errors.New("test IO failure")
	if err := copyImportTar(io.Discard, importErrorReader{failure}); !errors.Is(err, failure) {
		t.Errorf("read error = %v", err)
	}
	if err := copyImportTar(importErrorWriter{failure}, bytes.NewBufferString("uncompressed")); !errors.Is(err, failure) {
		t.Errorf("write error = %v", err)
	}
	for _, header := range [][]byte{{0x1f, 0x8b}, {'B', 'Z', 'h'}, {0xfd, '7', 'z', 'X', 'Z', 0}} {
		if err := copyImportTar(io.Discard, bytes.NewReader(header)); err == nil {
			t.Errorf("incomplete compression header accepted: %x", header)
		}
	}
	var out bytes.Buffer
	if err := copyImportTar(&out, bytes.NewBufferString("short")); err != nil || out.String() != "short" {
		t.Errorf("short plain input = %q, %v", out.String(), err)
	}
}
