// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/modelkey"
)

func writeAuthTokenFile(t *testing.T, access, refresh string, expiresAt time.Time) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "auth-token.json")
	b, _ := json.Marshal(map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_at":    expiresAt,
	})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LATERE_AUTH_TOKEN_FILE", p)
}

// TestModelsReplacesLux is criterion 3's command half: `latere models` is a
// command, and `latere lux` is an unknown one, with no alias or hidden
// command left behind.
func TestModelsReplacesLux(t *testing.T) {
	root := NewRoot("test")
	var models bool
	for _, c := range root.Commands() {
		if c.Name() == "models" {
			models = true
		}
		if c.Name() == "lux" || slices.Contains(c.Aliases, "lux") {
			t.Fatalf("command %q still answers to lux", c.Name())
		}
	}
	if !models {
		t.Fatal("'models' command not registered in root")
	}
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"lux", "models"})
	if _, err := captureStdout(root.Execute); err == nil || !strings.Contains(err.Error(), `unknown command "lux"`) {
		t.Fatalf("latere lux = %v, want unknown command", err)
	}
}

// TestNoSourceNamesTheHostedPlane is criterion 3's grep half: no Go source
// file, and no live document, names the hosted plane's host, its API path,
// or its base URL variable.
func TestNoSourceNamesTheHostedPlane(t *testing.T) {
	root := moduleRoot(t)
	// Built by concatenation so this file does not name them itself.
	retired := []string{"lux." + "latere.ai", "/lux" + "/v1/", "LUX_" + "API_URL"}
	var scanned int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			// .git and the worktrees of agents working in the checkout are
			// not the source tree.
			if rel != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		live := strings.HasSuffix(path, ".go") || rel == "README.md" || rel == "CONTRIBUTING.md" ||
			(strings.HasPrefix(rel, "docs"+string(filepath.Separator)) && strings.HasSuffix(path, ".md"))
		if !live {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, r := range retired {
			if bytes.Contains(b, []byte(r)) {
				t.Errorf("%s names %q", rel, r)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("scanned %d files; the walk did not reach the source tree", scanned)
	}
}

// TestDocsDescribeModels is criterion 5: the models guide names every
// command, the Lux guide is gone, and the README points at the guide.
func TestDocsDescribeModels(t *testing.T) {
	root := moduleRoot(t)
	guide, err := os.ReadFile(filepath.Join(root, "docs", "models.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"latere models", "latere models --json", "models env", "--provider anthropic",
		"--provider gemini", "--raw", "models invoke", "latere models key", "latere models key revoke",
		"LATERE_MODELS_URL", "LATERE_MODEL_KEY", "console's Models section"} {
		if !bytes.Contains(guide, []byte(want)) {
			t.Errorf("docs/models.md does not name %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "lux.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("docs/lux.md still exists: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(readme, []byte("`latere models`")) || !bytes.Contains(readme, []byte("docs/models.md")) {
		t.Error("README does not name `latere models` and its guide")
	}
}

// moduleRoot is the directory holding go.mod, above the test's directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

// TestResolveModelsURL is criterion 2: the origin by default,
// LATERE_MODELS_URL over it, the flag over both.
func TestResolveModelsURL(t *testing.T) {
	t.Setenv("LATERE_MODELS_URL", "")
	if got := resolveModelsURL(""); got != "https://api.latere.ai/v1/models" {
		t.Errorf("default = %q", got)
	}
	t.Setenv("LATERE_MODELS_URL", "http://env/v1/models/")
	if got := resolveModelsURL(""); got != "http://env/v1/models" {
		t.Errorf("env = %q", got)
	}
	if got := resolveModelsURL("http://flag/v1/models"); got != "http://flag/v1/models" {
		t.Errorf("flag = %q", got)
	}
}

// TestModelsURLReachesTheCommands is criterion 2 through the commands: the
// environment variable and the flag each select the core a command calls.
func TestModelsURLReachesTheCommands(t *testing.T) {
	w := newKeyWorld(t, "")
	other := newKeyWorld(t, "")
	t.Setenv("LATERE_MODELS_URL", other.modelsURL())

	root := NewRoot("test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"models", "--auth-url", w.srv.URL})
	if _, err := captureStdout(root.Execute); err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(other.coreCalls()) != 1 || len(w.coreCalls()) != 0 {
		t.Fatalf("LATERE_MODELS_URL: calls %d at the env core, %d at the other", len(other.coreCalls()), len(w.coreCalls()))
	}
	if _, _, err := w.run(t, "models", "list"); err != nil {
		t.Fatal(err)
	}
	if len(w.coreCalls()) != 1 || len(other.coreCalls()) != 1 {
		t.Fatalf("--models-url: calls %d at the flag core, %d at the env core", len(w.coreCalls()), len(other.coreCalls()))
	}
}

// TestModelsListPrintsNames is criterion 1 for `models` and `models list`:
// the key reaches the OpenAI door's model list, and the names print one per
// line, with where the prices are on stderr.
func TestModelsListPrintsNames(t *testing.T) {
	w := newKeyWorld(t, "")
	for _, args := range [][]string{{"models"}, {"models", "list"}} {
		out, errOut, err := w.run(t, args...)
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, errOut)
		}
		if out != strings.Join(stubModels, "\n")+"\n" {
			t.Errorf("%v printed %q", args, out)
		}
		if !strings.Contains(errOut, "console's Models section") {
			t.Errorf("%v stderr %q does not say where prices are", args, errOut)
		}
	}
	for _, c := range w.coreCalls() {
		if c.path != "/v1/models/openai/v1/models" || c.bearer != "pat_k1.value" {
			t.Fatalf("core call = %+v, want the list with the key", c)
		}
	}
	out, _, err := w.run(t, "models", "list", "--json")
	var got []modelEntry
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || len(got) != 2 || got[0].ID != stubModels[0] {
		t.Fatalf("--json = %q %v", out, err)
	}
}

// TestModelsListEmpty says so when the key reaches no model.
func TestModelsListEmpty(t *testing.T) {
	t.Setenv(modelkey.EnvKey, "pat_ci.value")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer srv.Close()
	for _, tc := range []struct{ json bool }{{false}, {true}} {
		root := NewRoot("test")
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(io.Discard)
		args := []string{"models", "--models-url", srv.URL}
		want := "No models.\n"
		if tc.json {
			args, want = append(args, "--json"), "[]\n"
		}
		root.SetArgs(args)
		if _, err := captureStdout(root.Execute); err != nil || out.String() != want {
			t.Fatalf("json=%t: %q %v, want %q", tc.json, out.String(), err, want)
		}
	}
}

// TestModelsEnvExportsEachDoor is criterion 1 for `models env`: each
// --provider exports its SDK's variables, the door under the base and the
// key, and an unknown one is refused before a key is resolved.
func TestModelsEnvExportsEachDoor(t *testing.T) {
	w := newKeyWorld(t, "")
	for _, tc := range []struct{ provider, base, key string }{
		{"", "OPENAI_BASE_URL=" + w.modelsURL() + "/openai/v1", "OPENAI_API_KEY=pat_k1.value"},
		{"openai", "OPENAI_BASE_URL=" + w.modelsURL() + "/openai/v1", "OPENAI_API_KEY=pat_k1.value"},
		{"anthropic", "ANTHROPIC_BASE_URL=" + w.modelsURL() + "/anthropic", "ANTHROPIC_API_KEY=pat_k1.value"},
		{"gemini", "GOOGLE_GEMINI_BASE_URL=" + w.modelsURL() + "/gemini", "GEMINI_API_KEY=pat_k1.value"},
	} {
		args := []string{"models", "env"}
		if tc.provider != "" {
			args = append(args, "--provider", tc.provider)
		}
		out, errOut, err := w.run(t, args...)
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, errOut)
		}
		want := "export " + tc.base + "\nexport " + tc.key + "\n"
		if out != want {
			t.Errorf("%v stdout = %q, want %q", args, out, want)
		}
		if !strings.HasPrefix(errOut, "# model key pat_k1") {
			t.Errorf("%v stderr = %q, want the key's provenance", args, errOut)
		}
	}
	created := len(w.requests)
	if _, _, err := w.run(t, "models", "env", "--provider", "cohere"); err == nil || !strings.Contains(err.Error(), "openai, anthropic, gemini") {
		t.Fatalf("unknown provider = %v", err)
	}
	if len(w.requests) != created {
		t.Fatal("an unknown provider created a key")
	}
}

// TestModelsInvokeCallsTheOpenAIDoor is criterion 1 for `models invoke`: one
// Chat Completions call with the key and the model as named.
func TestModelsInvokeCallsTheOpenAIDoor(t *testing.T) {
	w := newKeyWorld(t, "")
	out, errOut, err := w.run(t, "models", "invoke", "--model", "anthropic/claude-haiku-4.5", "Say", "hi")
	if err != nil || out != "hi\n" {
		t.Fatalf("invoke = %q %v %s", out, err, errOut)
	}
	calls := w.coreCalls()
	if len(calls) != 1 || calls[0].path != "/v1/models/openai/v1/chat/completions" ||
		calls[0].bearer != "pat_k1.value" || calls[0].model != "anthropic/claude-haiku-4.5" {
		t.Fatalf("core calls = %+v", calls)
	}
	if _, _, err := w.run(t, "models", "invoke", "hello"); err == nil || !strings.Contains(err.Error(), "--model is required") {
		t.Fatalf("no --model = %v", err)
	}
}

// TestModelsErrorsSayWhatToDo: a refusal a person acts on carries the next
// step, and the door's envelope is read into its code and sentence.
func TestModelsErrorsSayWhatToDo(t *testing.T) {
	t.Setenv(modelkey.EnvKey, "pat_ci.value")
	for _, tc := range []struct {
		status     int
		code, want string
	}{
		{http.StatusNotFound, "model_not_found", "latere models"},
		{http.StatusForbidden, "model_not_allowed", "latere models"},
		{http.StatusTooManyRequests, "budget_exhausted", "Billing section"},
		{http.StatusBadGateway, "upstream_error", "upstream_error: The provider returned an error."},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, `{"error":{"type":"x","code":"`+tc.code+`","message":"The provider returned an error."}}`)
		}))
		root := NewRoot("test")
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"models", "invoke", "--model", "m", "--models-url", srv.URL, "hi"})
		_, err := captureStdout(root.Execute)
		srv.Close()
		var apiErr *api.APIError
		if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.As(err, &apiErr) || apiErr.Code != tc.code {
			t.Errorf("%s: err = %v, want %q", tc.code, err, tc.want)
		}
	}
}

func TestModelsHelpText(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"models", "--help"}, []string{"https://api.latere.ai/v1/models", "LATERE_MODELS_URL", "latere login", "model key"}},
		{[]string{"models", "env", "--help"}, []string{"OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "--raw"}},
		{[]string{"models", "invoke", "--help"}, []string{"not an assistant", "latere topos --local", "--model"}},
		{[]string{"models", "list", "--help"}, []string{"console's Models section"}},
	}
	for _, tc := range cases {
		got, err := executeForHelp(NewRoot("test"), tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%v help missing %q\n%s", tc.args, w, got)
			}
		}
	}
}
