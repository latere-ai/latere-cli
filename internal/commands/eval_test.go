package commands

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// evalTestEnv pins the Eval auth environment for one test: a known
// token and a cleared EVAL_API_URL so a developer's shell cannot leak
// into assertions.
func evalTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("EVAL_ADMIN_TOKEN", "test-token")
	t.Setenv("EVAL_API_URL", "")
}

// runEvalCmd executes one `latere eval ...` subcommand and captures
// its stdout.
func runEvalCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newEvalCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

const evalApplyResponse = `{
  "dry_run": false,
  "suite": {"id": "st-1", "name": "frontier-suite", "status": "created"},
  "tasks": [
    {"id": "tk-1", "prompt_hash": "abcdef0123456789", "status": "created"},
    {"id": "tk-2", "prompt_hash": "fedcba9876543210", "status": "exists", "lineage_id": "ln-7"}
  ],
  "cells": {"created": 4, "exists": 2, "unmanaged": 1},
  "comparisons": [
    {"id": "cp-1", "name": "model-vs-model", "outcome": "created", "status": "single-variable", "members": 4},
    {"id": "cp-2", "name": "sloppy", "outcome": "updated", "status": "confounded(harness, effort_configured)", "confounds": ["harness", "effort_configured"], "members": 6}
  ],
  "warnings": ["matrix.exclude[0] matched no cells"]
}`

// TestEvalApplyResolvesFilePrompts locks the client-side prompt
// resolution contract: a file:// prompt ref is read relative to the
// manifest's directory and inlined as prompt_text, while the original
// prompt ref survives for provenance. It also asserts the wire shape:
// POST /api/v1/suites/apply, application/yaml, bearer auth, no
// dry_run param.
func TestEvalApplyResolvesFilePrompts(t *testing.T) {
	evalTestEnv(t)
	var (
		gotMethod string
		gotPath   string
		gotQuery  string
		gotCT     string
		gotAuth   string
		gotBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotCT = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(evalApplyResponse))
	}))
	defer srv.Close()

	dir := t.TempDir()
	const promptText = "Refactor the scheduler and keep the tests green.\n"
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "task1.md"), []byte(promptText), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `suite: frontier-suite
org: latere
tasks:
  - prompt: file://prompts/task1.md
    n: 3
  - prompt: inline prompt stays untouched
    n: 1
`
	manifestPath := filepath.Join(dir, "suite.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runEvalCmd(t, "apply", "-f", manifestPath, "--api-url", srv.URL)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/suites/apply" {
		t.Errorf("path = %q, want /api/v1/suites/apply", gotPath)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty (no dry_run)", gotQuery)
	}
	if !strings.HasPrefix(gotCT, "application/yaml") {
		t.Errorf("Content-Type = %q, want application/yaml", gotCT)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", gotAuth)
	}

	var posted struct {
		Tasks []struct {
			Prompt     string `yaml:"prompt"`
			PromptText string `yaml:"prompt_text"`
		} `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(gotBody, &posted); err != nil {
		t.Fatalf("posted body is not YAML: %v\n%s", err, gotBody)
	}
	if len(posted.Tasks) != 2 {
		t.Fatalf("posted %d tasks, want 2", len(posted.Tasks))
	}
	if posted.Tasks[0].Prompt != "file://prompts/task1.md" {
		t.Errorf("tasks[0].prompt = %q, want the original file:// ref retained", posted.Tasks[0].Prompt)
	}
	if posted.Tasks[0].PromptText != promptText {
		t.Errorf("tasks[0].prompt_text = %q, want the referenced file content %q", posted.Tasks[0].PromptText, promptText)
	}
	if posted.Tasks[1].Prompt != "inline prompt stays untouched" {
		t.Errorf("tasks[1].prompt = %q, want the inline prompt untouched", posted.Tasks[1].Prompt)
	}
	if posted.Tasks[1].PromptText != "" {
		t.Errorf("tasks[1].prompt_text = %q, want empty for inline prompts", posted.Tasks[1].PromptText)
	}

	for _, want := range []string{
		"suite frontier-suite (created)",
		"task abcdef012345 created",
		"task fedcba987654 exists (lineage ln-7)",
		"cells: 4 created, 2 exists, 1 unmanaged",
		"comparison model-vs-model: created, single-variable, 4 members",
		"comparison sloppy: updated, confounded(harness, effort_configured), 6 members",
		"warning: matrix.exclude[0] matched no cells",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("apply output missing %q\n%s", want, out)
		}
	}
}

// TestEvalApplyDryRun proves --dry-run adds ?dry_run=1 and the
// rendered diff is flagged as a dry run.
func TestEvalApplyDryRun(t *testing.T) {
	evalTestEnv(t)
	var gotDryRun string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDryRun = r.URL.Query().Get("dry_run")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Replace(evalApplyResponse, `"dry_run": false`, `"dry_run": true`, 1)))
	}))
	defer srv.Close()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "suite.yaml")
	if err := os.WriteFile(manifestPath, []byte("suite: s\norg: o\ntasks:\n  - prompt: hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runEvalCmd(t, "apply", "-f", manifestPath, "--dry-run", "--api-url", srv.URL)
	if err != nil {
		t.Fatalf("apply --dry-run: %v", err)
	}
	if gotDryRun != "1" {
		t.Errorf("dry_run query = %q, want 1", gotDryRun)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("output does not mark the dry run:\n%s", out)
	}
}

// TestEvalApplyStdin proves `-f -` reads the manifest from stdin and
// resolves file:// prompts relative to the current directory.
func TestEvalApplyStdin(t *testing.T) {
	evalTestEnv(t)
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(evalApplyResponse))
	}))
	defer srv.Close()

	dir := t.TempDir()
	const promptText = "prompt from cwd\n"
	if err := os.WriteFile(filepath.Join(dir, "p.md"), []byte(promptText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = w.WriteString("suite: s\norg: o\ntasks:\n  - prompt: file://p.md\n")
		_ = w.Close()
	}()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })

	if _, err := runEvalCmd(t, "apply", "-f", "-", "--api-url", srv.URL); err != nil {
		t.Fatalf("apply from stdin: %v", err)
	}
	if !strings.Contains(string(gotBody), "prompt from cwd") {
		t.Errorf("posted body did not inline the cwd-relative prompt:\n%s", gotBody)
	}
	if !strings.Contains(string(gotBody), "file://p.md") {
		t.Errorf("posted body dropped the original prompt ref:\n%s", gotBody)
	}
}

// TestEvalApplyMissingPromptFile proves a dangling file:// ref fails
// locally, before anything reaches the server.
func TestEvalApplyMissingPromptFile(t *testing.T) {
	evalTestEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server must not be reached when prompt resolution fails")
	}))
	defer srv.Close()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "suite.yaml")
	if err := os.WriteFile(manifestPath, []byte("tasks:\n  - prompt: file://nope.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runEvalCmd(t, "apply", "-f", manifestPath, "--api-url", srv.URL)
	if err == nil {
		t.Fatal("expected error for missing prompt file")
	}
	if !strings.Contains(err.Error(), "file://nope.md") {
		t.Errorf("error %q does not name the dangling ref", err)
	}
}

// TestEvalApplyErrorEnvelope proves evald's error envelope is
// rendered as "code: message" and surfaces as a non-nil (non-zero
// exit) error.
func TestEvalApplyErrorEnvelope(t *testing.T) {
	evalTestEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"unresolved-prompt","message":"tasks[0]: prompt is a file:// ref or empty"}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "suite.yaml")
	if err := os.WriteFile(manifestPath, []byte("suite: s\norg: o\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runEvalCmd(t, "apply", "-f", manifestPath, "--api-url", srv.URL)
	if err == nil {
		t.Fatal("expected error from the API envelope")
	}
	want := "unresolved-prompt: tasks[0]: prompt is a file:// ref or empty"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// TestEvalMissingToken proves every eval subcommand refuses to run
// without a token, naming both EVAL_ADMIN_TOKEN and --token.
func TestEvalMissingToken(t *testing.T) {
	t.Setenv("EVAL_ADMIN_TOKEN", "")
	t.Setenv("EVAL_API_URL", "")
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "suite.yaml")
	if err := os.WriteFile(manifestPath, []byte("suite: s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"apply":  {"apply", "-f", manifestPath},
		"suites": {"suites"},
		"cells":  {"cells", "--suite", "st-1"},
	} {
		_, err := runEvalCmd(t, args...)
		if err == nil {
			t.Fatalf("%s: expected missing-token error", name)
		}
		if !strings.Contains(err.Error(), "EVAL_ADMIN_TOKEN") || !strings.Contains(err.Error(), "--token") {
			t.Errorf("%s: error %q does not point at EVAL_ADMIN_TOKEN/--token", name, err)
		}
	}
}

// TestEvalSuitesTable locks the list endpoint and the aligned table
// rendering for `latere eval suites`.
func TestEvalSuitesTable(t *testing.T) {
	evalTestEnv(t)
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":"st-1","name":"frontier-suite","org":"latere","budget_cap_usd":250,"state":"active","created_at":"2026-07-01T00:00:00Z"},
			{"id":"st-2","name":"nightly","org":"latere","budget_cap_usd":42.5,"state":"paused","created_at":"2026-07-02T00:00:00Z"}
		]`))
	}))
	defer srv.Close()

	out, err := runEvalCmd(t, "suites", "--api-url", srv.URL)
	if err != nil {
		t.Fatalf("suites: %v", err)
	}
	if gotPath != "/api/v1/suites" {
		t.Errorf("path = %q, want /api/v1/suites", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", gotAuth)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want header + 2 rows:\n%s", len(lines), out)
	}
	for _, col := range []string{"ID", "NAME", "ORG", "BUDGET", "STATE"} {
		if !strings.Contains(lines[0], col) {
			t.Errorf("header %q missing column %q", lines[0], col)
		}
	}
	for _, want := range []string{"st-1", "frontier-suite", "$250.00", "active"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row 1 %q missing %q", lines[1], want)
		}
	}
	for _, want := range []string{"st-2", "nightly", "$42.50", "paused"} {
		if !strings.Contains(lines[2], want) {
			t.Errorf("row 2 %q missing %q", lines[2], want)
		}
	}
}

// TestEvalCellsTable locks the cells endpoint (suite filter as query
// param) and the tuple table rendering for `latere eval cells`.
func TestEvalCellsTable(t *testing.T) {
	evalTestEnv(t)
	var gotPath, gotSuite string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSuite = r.URL.Query().Get("suite")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":"ce-1","task_id":"tk-1","tuple":{"model_id":"fable-5","model_route":"anthropic","harness":"claude-code","harness_version":"3.1.0","image_tag":"ghcr.io/latere-ai/eval:1.2.3","effort_configured":"high","gateway_surface":"native"},"state":"pending"}
		]`))
	}))
	defer srv.Close()

	out, err := runEvalCmd(t, "cells", "--suite", "st-1", "--api-url", srv.URL)
	if err != nil {
		t.Fatalf("cells: %v", err)
	}
	if gotPath != "/api/v1/cells" {
		t.Errorf("path = %q, want /api/v1/cells", gotPath)
	}
	if gotSuite != "st-1" {
		t.Errorf("suite query = %q, want st-1", gotSuite)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want header + 1 row:\n%s", len(lines), out)
	}
	for _, col := range []string{"MODEL", "ROUTE", "HARNESS", "VERSION", "EFFORT", "SURFACE", "STATE"} {
		if !strings.Contains(lines[0], col) {
			t.Errorf("header %q missing column %q", lines[0], col)
		}
	}
	for _, want := range []string{"fable-5", "anthropic", "claude-code", "3.1.0", "high", "native", "pending"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row %q missing %q", lines[1], want)
		}
	}
}

// TestEvalCmdRegistered proves the eval group is wired into the root
// command next to the other product groups.
func TestEvalCmdRegistered(t *testing.T) {
	root := NewRoot("test")
	for _, c := range root.Commands() {
		if c.Name() == "eval" {
			return
		}
	}
	t.Fatal("root command does not register `latere eval`")
}
