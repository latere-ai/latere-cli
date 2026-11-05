// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"testing"
	"time"

	"latere.ai/x/sdk-go/evals"
	"latere.ai/x/sdk-go/oversight"
)

func TestPrintJSONIsIndentedSortedAndEndsInANewline(t *testing.T) {
	var buf bytes.Buffer
	v := map[string]any{
		"b":    []string{"x"},
		"a":    jsontext.Value(`{"k":1,"k":2}`),
		"text": "bad \xff byte <tag> & more",
	}
	if err := printJSON(&buf, v); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": {\n    \"k\": 1,\n    \"k\": 2\n  },\n  \"b\": [\n    \"x\"\n  ],\n  \"text\": \"bad � byte <tag> & more\"\n}\n"
	if buf.String() != want {
		t.Errorf("printJSON =\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestPrintRawJSONIndentsTheControlPlanesBytes(t *testing.T) {
	var buf bytes.Buffer
	if err := printRawJSON(&buf, []byte(`{"a":[1,2],"a":"<x>"}`)); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": [\n    1,\n    2\n  ],\n  \"a\": \"<x>\"\n}\n"
	if buf.String() != want {
		t.Errorf("printRawJSON = %q, want %q", buf.String(), want)
	}
	if err := printRawJSON(io.Discard, []byte(`{"a":`)); err == nil {
		t.Error("an answer that is no JSON was printed")
	}
}

func TestStampJSONKeepsNumbersAndTheMembersItDoesNotName(t *testing.T) {
	body := []byte(`{"spec":{"id":12345678901234567890,"note":"<a> & b","ratio":1.50},"metadata":{"name":"n","labels":{"keep":"1","mine":"old"}}}`)
	got, err := stampJSON(body, map[string]string{"mine": "new", "tenant": "t-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"metadata":{"labels":{"keep":"1","mine":"new","tenant":"t-1"},"name":"n"},"spec":{"id":12345678901234567890,"note":"<a> & b","ratio":1.50}}`
	if string(got) != want {
		t.Errorf("stamped manifest =\n%s\nwant\n%s", got, want)
	}
}

func TestStampJSONReadsAManifestTheWayItWasWritten(t *testing.T) {
	// A repeated key resolves to the last one, as it did for a manifest
	// written by hand; the control plane reads the same.
	got, err := stampJSON([]byte(`{"metadata":{"name":"first","name":"second"}}`), map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"metadata":{"labels":{"k":"v"},"name":"second"}}`
	if string(got) != want {
		t.Errorf("stamped manifest = %s, want %s", got, want)
	}
	// A body that carries every label already is answered as written.
	body := []byte(`{ "metadata": { "labels": { "k": "v" } } }`)
	if got, err := stampJSON(body, map[string]string{"k": "v"}); err != nil || !bytes.Equal(got, body) {
		t.Errorf("a stamped manifest = %s, %v", got, err)
	}
	// A body this cannot read as one object is answered as written.
	for _, body := range []string{`{"metadata":[]}`, `{"metadata":{"labels":"x"}}`, `{"a":1} {"b":2}`, `{"a":`} {
		if got, err := stampJSON([]byte(body), map[string]string{"k": "v"}); err != nil || string(got) != body {
			t.Errorf("stampJSON(%s) = %s, %v, want it as written", body, got, err)
		}
	}
}

func TestWithSecretValueSendsBackTheObjectWithoutItsStatus(t *testing.T) {
	raw := []byte(`{"status":{"workloads":2},"spec":{"value":"old","big":12345678901234567890,"type":"bearer"},"metadata":{"name":"s","labels":{"a":"<b>"}}}`)
	got, err := withSecretValue(raw, "new&<value>")
	if err != nil {
		t.Fatal(err)
	}
	// Members the CLI does not open are sent as they were read.
	want := `{"metadata":{"name":"s","labels":{"a":"<b>"}},"spec":{"big":12345678901234567890,"type":"bearer","value":"new&<value>"}}`
	if string(got) != want {
		t.Errorf("rotation body =\n%s\nwant\n%s", got, want)
	}
	for _, bad := range []string{`[]`, `{"spec":null}`, `{"spec":1}`, `{}`} {
		if _, err := withSecretValue([]byte(bad), "v"); err == nil {
			t.Errorf("withSecretValue(%s) succeeded", bad)
		}
	}
}

func TestForeignInputIsReadAsItComes(t *testing.T) {
	// A model's tool-call input may repeat a key, carry bytes that are not
	// UTF-8 and write a name in any case.
	if got := inputSummary("bash", jsontext.Value("{\"Command\":\"ls\",\"command\":\"go test \xff\"}")); got != "go test �" {
		t.Errorf("inputSummary = %q", got)
	}
	if got := summarizeToolInput(jsontext.Value("{\"path\":\"a.go\",\"path\":\"b\xff.go\"}")); got != " (b\uFFFD.go)" {
		t.Errorf("summarizeToolInput = %q", got)
	}
	e := parsePlatformError(409, []byte(`{"Code":"exists","Message":"It exists.","message":"It exists twice."}`))
	if e.Code != "exists" || e.Message != "It exists twice." {
		t.Errorf("parsePlatformError = %+v", e)
	}
	e = parsePlatformError(409, []byte(`{"Error":{"Code":"nested","Message":"Deep."}}`))
	if e.Code != "nested" || e.Message != "Deep." {
		t.Errorf("parsePlatformError nested = %+v", e)
	}
}

func TestFilterRawKeepsTheOtherMembersInKeyOrder(t *testing.T) {
	raw := jsontext.Value(`{"z":1,"items":[{"i":0},{"i":1},{"i":2}],"a":{"x":"<"}}`)
	got, err := filterRaw(raw, "items", func(i int) bool { return i != 1 })
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"x":"<"},"items":[{"i":0},{"i":2}],"z":1}`
	if string(got) != want {
		t.Errorf("filterRaw = %s, want %s", got, want)
	}
}

func TestEvalsFrameOmitsOnlyAbsentData(t *testing.T) {
	frame := func(data jsontext.Value) string {
		var buf bytes.Buffer
		w := &evalsWatch{out: &buf, jsonOut: true, now: time.Now}
		if err := w.printFrame(&evals.StreamEvent{Seq: 7, Type: "cell", Data: data}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	for _, tc := range []struct {
		data jsontext.Value
		want string
	}{
		{nil, "{\"id\":\"7\",\"event\":\"cell\"}\n"},
		{jsontext.Value{}, "{\"id\":\"7\",\"event\":\"cell\"}\n"},
		{jsontext.Value(`null`), "{\"id\":\"7\",\"event\":\"cell\",\"data\":null}\n"},
		{jsontext.Value(`{}`), "{\"id\":\"7\",\"event\":\"cell\",\"data\":{}}\n"},
		{jsontext.Value(`{"a":1}`), "{\"id\":\"7\",\"event\":\"cell\",\"data\":{\"a\":1}}\n"},
	} {
		if got := frame(tc.data); got != tc.want {
			t.Errorf("frame(%s) = %q, want %q", tc.data, got, tc.want)
		}
	}
}

func TestToolCallOmitsOnlyAnAbsentAnswer(t *testing.T) {
	for _, tc := range []struct {
		answer jsontext.Value
		want   string
	}{
		{nil, `{"seq":1,"time":"2026-10-14T09:30:00Z","action_id":"a","tool_use":{"n":1}}`},
		{jsontext.Value(`{}`), `{"seq":1,"time":"2026-10-14T09:30:00Z","action_id":"a","tool_use":{"n":1},"answer":{}}`},
		{jsontext.Value(`null`), `{"seq":1,"time":"2026-10-14T09:30:00Z","action_id":"a","tool_use":{"n":1},"answer":null}`},
	} {
		got, err := json.Marshal(&toolCall{Seq: 1, Time: jsonBenchTime, ActionID: "a", ToolUse: jsontext.Value(`{"n":1}`), Answer: tc.answer})
		if err != nil || string(got) != tc.want {
			t.Errorf("toolCall with answer %s = %s, %v, want %s", tc.answer, got, err, tc.want)
		}
	}
}

func TestAnswerParamsOmitRememberedWhenFalse(t *testing.T) {
	params := &oversight.AnswerParams{Org: "o", Subject: "s", ActionID: "a", Approve: true}
	var buf bytes.Buffer
	err := printJSON(&buf, struct {
		*oversight.AnswerParams
		Remembered bool `json:"remembered,omitzero"`
	}{params, params.Remembered})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"org\": \"o\",\n  \"subject\": \"s\",\n  \"action_id\": \"a\",\n  \"approve\": true\n}\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
}
