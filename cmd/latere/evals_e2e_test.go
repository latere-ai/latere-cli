// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestEvalsE2E drives the built binary against a stub of the Evals API
// under its origin base path and of auth's mint: every request carries an
// actor token minted for examen from the saved login, a list prints the
// suites, show names an experiment's definitions and prints its progress, a
// watch follows the stream to an end where every cell completed and exits
// 0, a canceled experiment's watch exits 1, and a refusal exits 2.
func TestEvalsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	digest := func(d string) string { return "sha256:" + strings.Repeat(d, 64) }
	experiment := func(id, state string) string {
		return `{"id":"` + id + `","owner":"o-1","suite":"` + digest("1") + `","candidates":["` + digest("a") + `"],"trials":3,"parallelism":2,
"reference":{"candidate":"` + digest("a") + `"},"mode":"execute","state":"` + state + `","budget":{"name":"eb-1","cap_usd_micro":5000000},
"progress":{"cells":{"` + state + `":1},"trials":{"done":3},"outcomes":{"completed":3}},"created_at":"2026-10-20T10:00:00Z"}`
	}
	var (
		mu        sync.Mutex
		audiences []string
		bearers   []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/actor-tokens" {
			var body struct {
				Audience string `json:"audience"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("mint body: %v", err)
			}
			mu.Lock()
			audiences = append(audiences, body.Audience)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"actor_token":"evals-actor","expires_in":300}`)
			return
		}
		mu.Lock()
		bearers = append(bearers, r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/evals/suites":
			_, _ = io.WriteString(w, `{"items":[{"id":"sui_1","owner":"o-1","name":"fix-flaky-tests","digest":"`+digest("1")+`","current":true,
"created_at":"2026-10-20T10:00:00Z","tasks":["`+digest("c")+`"],"metrics":["builtin:completed"],"primary_metric":"builtin:completed"}]}`)
		case "GET /v1/evals/suites/" + digest("1"):
			_, _ = io.WriteString(w, `{"id":"sui_1","name":"fix-flaky-tests","digest":"`+digest("1")+`","current":true,"created_at":"2026-10-20T10:00:00Z"}`)
		case "GET /v1/evals/candidates/" + digest("a"):
			_, _ = io.WriteString(w, `{"id":"can_1","name":"acme-1-4","digest":"`+digest("a")+`","current":true,"created_at":"2026-10-20T10:00:00Z"}`)
		case "GET /v1/evals/experiments/exp_1":
			_, _ = io.WriteString(w, experiment("exp_1", "complete"))
		case "GET /v1/evals/experiments/exp_2":
			_, _ = io.WriteString(w, experiment("exp_2", "canceled"))
		case "GET /v1/evals/experiments/exp_1/comparisons":
			_, _ = io.WriteString(w, `{"items":[]}`)
		case "GET /v1/evals/experiments/exp_1/stream", "GET /v1/evals/experiments/exp_2/stream":
			id := strings.Split(r.URL.Path, "/")[4]
			state := map[string]string{"exp_1": "complete", "exp_2": "canceled"}[id]
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "id: 2\nevent: snapshot\ndata: "+strings.ReplaceAll(experiment(id, "running"), "\n", "")+"\n\n"+
				": keepalive\n\nid: 3\nevent: experiment\ndata: {\"id\":\""+id+"\",\"state\":\""+state+"\",\"progress\":{\"cells\":{\""+state+"\":1},\"trials\":{\"done\":3}}}\n\n"+
				"event: end\ndata: {\"state\":\""+state+"\"}\n\n")
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"experiment_not_found","message":"This experiment does not exist, or you do not have access to it.","details":{"retryable":false}}}`)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	authPath := filepath.Join(root, "auth-token.json")
	login, err := json.Marshal(map[string]any{"access_token": "saved-login", "expires_at": time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, login, 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "LATERE_EVALS_URL="+server.URL+"/v1/evals", "LATERE_EVALS_TOKEN=", "AUTH_URL="+server.URL,
		"LATERE_AUTH_TOKEN_FILE="+authPath, "LATERE_PLATFORM_URL=https://console.example",
		"LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+root)
	run := func(args ...string) (string, string, int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append([]string{"evals"}, args...)...)
		command.Env = env
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		code := 0
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
		return stdout.String(), stderr.String(), code
	}

	out, errOut, code := run("suites", "list")
	if code != 0 || !strings.Contains(out, "fix-flaky-tests") || !strings.Contains(out, "builtin:completed") {
		t.Errorf("suites list = %d %q %q", code, out, errOut)
	}
	out, errOut, code = run("experiments", "show", "exp_1")
	for _, want := range []string{"suite:      fix-flaky-tests@" + digest("1"), "candidates: acme-1-4@" + digest("a"), "budget:     $5.00 cap (eb-1)",
		"outcomes:   completed 3", "console:    https://console.example/console/evals/experiments/exp_1"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("experiments show = %d %q, lacks %q:\n%s", code, errOut, want, out)
		}
	}
	out, errOut, code = run("experiments", "watch", "exp_1")
	if code != 0 || !strings.Contains(out, "experiment exp_1 running") || !strings.Contains(out, "experiment exp_1 complete · cells complete 1") {
		t.Errorf("watch to complete = %d %q:\n%s", code, errOut, out)
	}
	_, errOut, code = run("experiments", "watch", "exp_2")
	if code != 1 || !strings.Contains(errOut, "exp_2 was canceled") {
		t.Errorf("watch to canceled = %d %q, want 1", code, errOut)
	}
	_, errOut, code = run("experiments", "show", "exp_nope")
	if code != 2 || !strings.Contains(errOut, "experiment_not_found: This experiment does not exist") {
		t.Errorf("show of a missing experiment = %d %q, want 2", code, errOut)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(audiences) == 0 || slices.ContainsFunc(audiences, func(a string) bool { return a != "examen" }) {
		t.Errorf("minted audiences = %q, want examen alone", audiences)
	}
	if slices.ContainsFunc(bearers, func(b string) bool { return b != "Bearer evals-actor" }) {
		t.Errorf("bearers = %q, want the actor token alone, never the login", bearers)
	}
}
