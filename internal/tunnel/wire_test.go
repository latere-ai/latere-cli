// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package tunnel

import (
	"encoding/json"
	"testing"
)

// TestDescriptorWireFormat pins the exact JSON the handshake descriptor
// marshals to. The Lux gateway holds an identical golden test against the
// SAME bytes; if either side renames or retags a field, its golden breaks,
// catching a wire drift that the mock-based round-trip tests (each side
// fakes the other) would miss.
//
// THIS STRING MUST STAY BYTE-IDENTICAL TO THE GATEWAY'S GOLDEN.
const goldenDescriptorJSON = `{"node_id":"node-1","runtime":"ollama","dialect":"openai-compat","base_url":"http://localhost:11434","models":["llama3.1:8b","qwen2.5:14b"],"share":"owner"}`

func TestDescriptorWireFormat(t *testing.T) {
	d := Descriptor{
		NodeID:  "node-1",
		Runtime: "ollama",
		Dialect: "openai-compat",
		BaseURL: "http://localhost:11434",
		Models:  []string{"llama3.1:8b", "qwen2.5:14b"},
		Share:   "owner",
	}
	got, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != goldenDescriptorJSON {
		t.Errorf("descriptor wire format drifted:\n got: %s\nwant: %s", got, goldenDescriptorJSON)
	}
}
