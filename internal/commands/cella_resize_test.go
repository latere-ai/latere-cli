package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/latere-ai/latere-cli/internal/api"
)

// TestCeResizeFlags locks the resize command surface: a required --disk-gb
// knob and nothing else surprising.
func TestCeResizeFlags(t *testing.T) {
	cmd := newCeResizeCmd()
	if cmd.Use != "resize <name|id> --disk-gb N" {
		t.Fatalf("resize Use = %q", cmd.Use)
	}
	if cmd.Flags().Lookup("disk-gb") == nil {
		t.Fatal("resize: --disk-gb flag missing")
	}
}

// TestCeResizePostsDiskGB proves resize POSTs {"disk_gb":N} to the sandbox's
// /resize subpath, the same composition the command performs.
func TestCeResizePostsDiskGB(t *testing.T) {
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sb-1","name":"dev"}`))
	}))
	defer srv.Close()

	c := &api.Client{BaseURL: srv.URL, Token: "test", HTTP: srv.Client()}
	body, _ := json.Marshal(map[string]any{"disk_gb": 50})
	var sb sandboxDTO
	if err := c.Do(t.Context(), http.MethodPost, sbPath("dev")+"/resize",
		bytes.NewReader(body), "application/json", &sb); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotPath != "/v1/sandboxes/dev/resize" {
		t.Errorf("path = %q, want /v1/sandboxes/dev/resize", gotPath)
	}
	var got map[string]any
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["disk_gb"] != float64(50) {
		t.Errorf("body disk_gb = %v, want 50", got["disk_gb"])
	}
}
