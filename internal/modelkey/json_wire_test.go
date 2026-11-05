// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package modelkey

import (
	"os"
	"testing"
	"time"
)

func TestKeyFileIsWrittenIndentedInSlotOrder(t *testing.T) {
	f := File{Path: t.TempDir() + "/model-keys.json"}
	at := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	if err := f.write(map[string]Record{"b": {ID: "2", CreatedAt: at, ExpiresAt: at}, "a": {ID: "1", CreatedAt: at, ExpiresAt: at}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	rec := func(id, ind string) string {
		return "{\n" + ind + "  \"id\": \"" + id + "\",\n" + ind + "  \"prefix\": \"\",\n" + ind + "  \"value\": \"\",\n" + ind + "  \"auth_base\": \"\",\n" + ind + "  \"sub\": \"\",\n" + ind + "  \"context\": \"\",\n" + ind + "  \"status\": \"\",\n" + ind + "  \"created_at\": \"2026-10-14T09:30:00Z\",\n" + ind + "  \"expires_at\": \"2026-10-14T09:30:00Z\"\n" + ind + "}"
	}
	want := "{\n  \"a\": " + rec("1", "  ") + ",\n  \"b\": " + rec("2", "  ") + "\n}"
	if string(got) != want {
		t.Errorf("model-keys.json =\n%s\nwant\n%s", got, want)
	}
}

func TestKeyFileIsReadAsAPersonEditedIt(t *testing.T) {
	f := File{Path: t.TempDir() + "/model-keys.json"}
	body := "{\"slot\":{\"ID\":\"first\",\"id\":\"second\",\"Value\":\"v\xff\"}}"
	if err := os.WriteFile(f.Path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r, ok, err := f.Get("slot")
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if r.ID != "second" || r.Value != "v�" {
		t.Errorf("record = %+v", r)
	}
}
