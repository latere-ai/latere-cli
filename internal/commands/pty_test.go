package commands

import "testing"

func TestPTYWebSocketURL(t *testing.T) {
	got, err := ptyWebSocketURL("https://cella.latere.ai", "demo", "cli-1")
	if err != nil {
		t.Fatal(err)
	}
	want := "wss://cella.latere.ai/v1/sandboxes/demo/sessions/cli-1"
	if got != want {
		t.Fatalf("ptyWebSocketURL = %q, want %q", got, want)
	}
}

func TestPTYWebSocketURLWithBasePathAndEscaping(t *testing.T) {
	got, err := ptyWebSocketURL("http://localhost:8080/api", "name with spaces", "cli/session")
	if err != nil {
		t.Fatal(err)
	}
	want := "ws://localhost:8080/api/v1/sandboxes/name%20with%20spaces/sessions/cli%2Fsession"
	if got != want {
		t.Fatalf("ptyWebSocketURL = %q, want %q", got, want)
	}
}
