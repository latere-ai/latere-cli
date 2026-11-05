// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"latere.ai/x/sdk-go/evals"
)

// jsonBenchSizes are the sub-benchmarks of a path whose payload grows with a
// listing: the item count that makes about 0.5 KB, 10 KB and 200 KB.
var jsonBenchSizes = []struct {
	name string
	n    int
}{{"small", 1}, {"medium", 20}, {"large", 400}}

var jsonBenchTime = time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)

// jsonBenchEvalsRaws is a --json list of n evals versions as the API answered.
func jsonBenchEvalsRaws(n int) []evalsRow {
	rows := make([]evalsRow, n)
	for i := range rows {
		raw := fmt.Sprintf(`{"id":"ver_01JA3BQ7Z4M8N2P5R6S7%04d","owner":"org_01HZX3K9T5W2","name":"checkout-regression-%d","digest":"sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08","current":true,"created_at":"2026-10-14T09:30:00Z","kind":"suite","cases":[{"id":"c1","prompt":"Refund the order and explain the policy","expect":"mentions the 30 day window"},{"id":"c2","prompt":"Cancel my subscription","expect":"asks for confirmation"}]}`, i, i)
		rows[i] = evalsRow{key: fmt.Sprintf("checkout-regression-%d", i), raw: json.RawMessage(raw)}
	}
	return rows
}

// BenchmarkJSONEvalsListEncode is `latere evals <kind> list --json`: the
// versions as the API answered, printed indented by printJSON.
func BenchmarkJSONEvalsListEncode(b *testing.B) {
	for _, sz := range jsonBenchSizes {
		b.Run(sz.name, func(b *testing.B) {
			items := raws(jsonBenchEvalsRaws(sz.n))
			var buf bytes.Buffer
			if err := printJSON(&buf, items); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(buf.Len()))
			for b.Loop() {
				if err := printJSON(io.Discard, items); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// jsonBenchToolCalls is the tool calls of a hosted session log.
func jsonBenchToolCalls(n int) []*toolCall {
	calls := make([]*toolCall, n)
	for i := range calls {
		use := fmt.Sprintf(`{"tool_use_id":"toolu_01A9Q%05d","name":"bash","input":{"command":"go test ./internal/... -run TestCheckout -count=1 -v","description":"Run the checkout tests"}}`, i)
		calls[i] = &toolCall{
			Seq:      int64(i*2 + 1),
			Time:     jsonBenchTime.Add(time.Duration(i) * time.Second),
			ActionID: fmt.Sprintf("act_ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX_toolu_01A9Q%05d", i),
			ToolUse:  json.RawMessage(use),
			Answer:   json.RawMessage(`{"tool_use_id":"toolu_01A9Q00000","result":"allow","message":"ok"}`),
		}
	}
	return calls
}

// BenchmarkJSONSessionLogEncode is `latere agents session <id> --json`: the
// session as the API answered it with its tool calls and hand-backs.
func BenchmarkJSONSessionLogEncode(b *testing.B) {
	session := json.RawMessage(`{"id":"ses_01JA3BQ7Z4M8N2P5R6S7T8V9WX","agent_id":"agt_01HZX3K9T5W2","status":"idle","created_at":"2026-10-14T09:30:00Z","title":"Fix the checkout rounding bug"}`)
	for _, sz := range []struct {
		name  string
		calls int
	}{{"small", 1}, {"medium", 25}, {"large", 600}} {
		b.Run(sz.name, func(b *testing.B) {
			doc := map[string]any{
				"session":    session,
				"tool_calls": jsonBenchToolCalls(sz.calls),
				"hand_backs": []*handBack{{Seq: 9, Time: jsonBenchTime, Payload: json.RawMessage(`{"limit":"consecutive","consecutive":3,"total":3,"effect":"ask"}`)}},
			}
			var buf bytes.Buffer
			if err := printJSON(&buf, doc); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(buf.Len()))
			for b.Loop() {
				if err := printJSON(io.Discard, doc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkJSONToolInputSummaryUnmarshal is the one-line summary of a tool
// call's input that `latere agents session` prints per call.
func BenchmarkJSONToolInputSummaryUnmarshal(b *testing.B) {
	input := json.RawMessage(`{"command":"go test ./internal/... -run TestCheckout -count=1 -v","description":"Run the checkout tests","timeout":120000}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	for b.Loop() {
		if inputSummary("bash", input) == "" {
			b.Fatal("empty summary")
		}
	}
}

// BenchmarkJSONEvalsWatchFrameMarshal is one line of `latere evals watch
// --json`: a progress event framed with its id and event name.
func BenchmarkJSONEvalsWatchFrameMarshal(b *testing.B) {
	data := json.RawMessage(`{"cell":"model-a/suite-1","state":"running","trials_done":12,"trials":30,"passed":10,"failed":2,"updated_at":"2026-10-14T09:30:00Z"}`)
	ev := &evals.StreamEvent{Seq: 4821, Type: "cell", Data: data}
	w := &evalsWatch{out: io.Discard, jsonOut: true, now: func() time.Time { return jsonBenchTime }}
	var buf bytes.Buffer
	w.out = &buf
	if err := w.printFrame(ev); err != nil {
		b.Fatal(err)
	}
	w.out = io.Discard
	b.ReportAllocs()
	b.SetBytes(int64(buf.Len()))
	for b.Loop() {
		if err := w.printFrame(ev); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJSONPlatformErrorUnmarshal reads a refusal body of the platform in
// each of its two envelopes, as every failed call of the memory, repos and
// account commands does.
func BenchmarkJSONPlatformErrorUnmarshal(b *testing.B) {
	for _, tc := range []struct{ name, body string }{
		{"flat", `{"code":"not_found","message":"No repository named checkout-service in this organization.","detail":"Check the name with latere repos list."}`},
		{"nested", `{"error":{"code":"quota_exceeded","message":"The organization has reached its repository limit.","details":{"limit":50,"used":50,"detail":"Delete a repository or ask an owner to raise the limit."}}}`},
	} {
		b.Run(tc.name, func(b *testing.B) {
			body := []byte(tc.body)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if e := parsePlatformError(429, body); e.Code == "" {
					b.Fatal("no code")
				}
			}
		})
	}
}

// jsonBenchMemoryList is the answer of the memory list call: n memories, a
// few projects and the settings object.
func jsonBenchMemoryList(n int) json.RawMessage {
	var sb strings.Builder
	sb.WriteString(`{"memories":[`)
	for i := range n {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":"mem_01JA3BQ7Z4M8N%05d","project_id":"prj_%d","kind":"feedback","text":"Prefer table driven tests and keep the fixtures next to the test. Run the linter before pushing, never after the review.","created_at":"2026-10-14T09:30:00Z","updated_at":"2026-10-14T09:30:00Z"}`, i, i%3)
	}
	sb.WriteString(`],"projects":[{"id":"prj_0","name":"checkout"},{"id":"prj_1","name":"billing"},{"id":"prj_2","name":"mobile"}],"settings":{"enabled":true,"tidy":false,"retention":"90d","scope":"project","share":true,"notes":"on"}}`)
	return json.RawMessage(sb.String())
}

// BenchmarkJSONMemoryListFilterRoundTrip is `latere memory list --project
// <name> --json`: the platform's answer decoded, filtered to one project and
// encoded again with its other members kept.
func BenchmarkJSONMemoryListFilterRoundTrip(b *testing.B) {
	for _, sz := range []struct {
		name string
		n    int
	}{{"small", 3}, {"medium", 60}, {"large", 1200}} {
		b.Run(sz.name, func(b *testing.B) {
			raw := jsonBenchMemoryList(sz.n)
			keep := func(i int) bool { return i%3 == 0 }
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				if _, err := filterRaw(raw, "memories", keep); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkJSONMemorySettingsDecode is `latere memory settings`: the settings
// object read member by member in the platform's order.
func BenchmarkJSONMemorySettingsDecode(b *testing.B) {
	obj := json.RawMessage(`{"enabled":true,"tidy":false,"retention":"90d","scope":"project","share":true,"notes":"on","updated_at":"2026-10-14T09:30:00Z"}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(obj)))
	for b.Loop() {
		if _, err := readSwitches(obj); err != nil {
			b.Fatal(err)
		}
	}
}
