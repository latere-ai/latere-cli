// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package modelkey

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func jsonBenchRecords(n int) map[string]Record {
	m := make(map[string]Record, n)
	for i := range n {
		slot := fmt.Sprintf("slot-%d", i)
		m[slot] = Record{
			ID:        fmt.Sprintf("key_01JA3BQ7Z4M8N2P5R6S7%04d", i),
			Prefix:    "lk_live_9f3a",
			Value:     "lk_live_9f3a8c1d5e7b2a4c6e8f0a1b3c5d7e9f1a2b4c6d8e0f",
			AuthBase:  "https://auth.latere.ai",
			Sub:       "usr_01JA3BQ7Z4M8N2P5R6S7T8V9WX",
			Context:   PersonalContext,
			Status:    "active",
			CreatedAt: time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC),
			ExpiresAt: time.Date(2027, 10, 14, 9, 30, 0, 0, time.UTC),
		}
	}
	return m
}

// BenchmarkJSONKeyFileGetUnmarshal is File.Get, which reads the whole
// model-keys.json to find one slot; every command that needs a model key does.
func BenchmarkJSONKeyFileGetUnmarshal(b *testing.B) {
	for _, sz := range []struct {
		name string
		n    int
	}{{"small", 1}, {"medium", 25}} {
		b.Run(sz.name, func(b *testing.B) {
			enc, err := json.MarshalIndent(jsonBenchRecords(sz.n), "", "  ")
			if err != nil {
				b.Fatal(err)
			}
			f := File{Path: filepath.Join(b.TempDir(), "model-keys.json")}
			if err := os.WriteFile(f.Path, enc, 0o600); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(enc)))
			for b.Loop() {
				if _, ok, err := f.Get("slot-0"); err != nil || !ok {
					b.Fatal("get failed", err)
				}
			}
		})
	}
}

// BenchmarkJSONKeyFilePutMarshal is the encoding File.Put writes to
// model-keys.json; the write itself syncs to disk and is left out.
func BenchmarkJSONKeyFilePutMarshal(b *testing.B) {
	m := jsonBenchRecords(5)
	enc, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(enc)))
	for b.Loop() {
		if _, err := json.MarshalIndent(m, "", "  "); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJSONKeychainRecordRoundTrip is the record Keychain.Put and
// Keychain.Get store as one entry's secret.
func BenchmarkJSONKeychainRecordRoundTrip(b *testing.B) {
	r := jsonBenchRecords(1)["slot-0"]
	enc, err := json.Marshal(r)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(enc)))
	for b.Loop() {
		out, err := json.Marshal(r)
		if err != nil {
			b.Fatal(err)
		}
		var back Record
		if err := json.Unmarshal(out, &back); err != nil {
			b.Fatal(err)
		}
	}
}
