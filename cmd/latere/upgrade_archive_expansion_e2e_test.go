// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

func TestUpgradeArchiveExpansionE2E(t *testing.T) {
	testUpgradeRejectsInvalidArchive(t, "BINARY-BYTES", "expanded archive exceeds", func(archive []byte) []byte {
		reader, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			t.Fatal(err)
		}
		original, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		var expanded bytes.Buffer
		gz, err := gzip.NewWriterLevel(&expanded, gzip.BestSpeed)
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(gz)
		const prefixBytes = 400 << 20
		if err := tw.WriteHeader(&tar.Header{Name: "README", Size: prefixBytes, Mode: 0644}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(tw, archiveZeroReader{}, prefixBytes); err != nil {
			t.Fatal(err)
		}
		if err := tw.Flush(); err != nil {
			t.Fatal(err)
		}
		// Append the original entries and end markers after the prefix entry;
		// closing tw here would put an end marker before the binary.
		if _, err := gz.Write(original); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		return expanded.Bytes()
	})
}

type archiveZeroReader struct{}

func (archiveZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
