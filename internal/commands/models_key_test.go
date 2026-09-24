// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/modelkey"
)

// The model key (specs/006-model-key.md) and the model commands over it
// (specs/007-models-at-the-origin.md), against a stub that is both auth's
// /me/keys and the Lux core's OpenAI door at /v1/models/openai.

// stubModels is the model list the stub core answers.
var stubModels = []string{"anthropic/claude-sonnet-4.6", "openai/gpt-4.1-mini"}

// coreCall is one model call the stub core received.
type coreCall struct {
	path, bearer, model string
	stream              bool
}

type keyWorld struct {
	srv      *httptest.Server
	store    modelkey.File
	mu       sync.Mutex
	requests []map[string]any
	revoked  []string
	core     []coreCall
	// status is what POST /me/keys answers: "active", "pending_approval",
	// or an auth error code.
	status string
	// accept decides whether the core accepts a bearer on this call.
	accept func(bearer string, call int32) bool
	calls  atomic.Int32
	n      int
}

// modelsURL is the stub core's base, the one the origin serves at
// /v1/models.
func (w *keyWorld) modelsURL() string { return w.srv.URL + "/v1/models" }

// coreCalls answers the model calls the stub core received.
func (w *keyWorld) coreCalls() []coreCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]coreCall(nil), w.core...)
}

func newKeyWorld(t *testing.T, orgID string) *keyWorld {
	t.Helper()
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	t.Setenv(modelkey.EnvKey, "")
	t.Setenv("LATERE_MODELS_URL", "")
	w := &keyWorld{status: "active", accept: func(string, int32) bool { return true }}
	w.store = modelkey.File{Path: filepath.Join(t.TempDir(), "model-keys.json")}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/me/keys":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode: %v", err)
			}
			w.mu.Lock()
			w.requests = append(w.requests, body)
			w.n++
			id := "k" + string(rune('0'+w.n))
			status := w.status
			w.mu.Unlock()
			if status != "active" && status != "pending_approval" {
				rw.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(rw, `{"error":"`+status+`","message":"refused"}`)
				return
			}
			rw.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(rw).Encode(map[string]any{
				"id": id, "prefix": "pat_" + id, "key": "pat_" + id + ".value", "status": status,
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/me/keys/"):
			w.mu.Lock()
			w.revoked = append(w.revoked, strings.TrimPrefix(r.URL.Path, "/me/keys/"))
			w.mu.Unlock()
			rw.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/v1/models/openai/v1/"):
			w.serveCore(t, rw, r)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(w.srv.Close)

	claims := map[string]any{"sub": "u1", "principal_type": "user", "org_id": orgID,
		"exp": time.Now().Add(time.Hour).Unix()}
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "auth-token.json"))
	if err := api.SaveAuthToken(api.Token{AccessToken: fakeJWT(t, claims), TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	prev := newModelKeys
	newModelKeys = func() *modelkey.Keys {
		return &modelkey.Keys{Store: w.store, Auth: modelkey.DefaultAuth, Now: time.Now, Host: "laptop"}
	}
	t.Cleanup(func() { newModelKeys = prev })
	prevWait := retryFirstWait
	retryFirstWait = 10 * time.Millisecond
	t.Cleanup(func() { retryFirstWait = prevWait })
	return w
}

// serveCore is the stub core's OpenAI door: the model list, and Chat
// Completions answered whole or as an event stream.
func (w *keyWorld) serveCore(t *testing.T, rw http.ResponseWriter, r *http.Request) {
	n := w.calls.Add(1)
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode core body: %v", err)
		}
	}
	w.mu.Lock()
	w.core = append(w.core, coreCall{path: r.URL.Path, bearer: bearer, model: body.Model, stream: body.Stream})
	w.mu.Unlock()
	if !w.accept(bearer, n) {
		rw.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(rw, `{"error":{"code":"unauthenticated","message":"This request needs a valid credential."}}`)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models/openai/v1/models":
		var data []string
		for _, m := range stubModels {
			data = append(data, fmt.Sprintf(`{"id":%q,"object":"model","created":0,"owned_by":"lux"}`, m))
		}
		_, _ = io.WriteString(rw, `{"object":"list","data":[`+strings.Join(data, ",")+`]}`)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/models/openai/v1/chat/completions" && body.Stream:
		rw.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"id":"c1","object":"chat.completion.chunk","model":"` + body.Model + `","choices":[{"index":0,"delta":{"role":"assistant","content":"no findings"},"finish_reason":null}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"` + body.Model + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`[DONE]`,
		} {
			_, _ = io.WriteString(rw, "data: "+chunk+"\n\n")
		}
	case r.Method == http.MethodPost && r.URL.Path == "/v1/models/openai/v1/chat/completions":
		_, _ = io.WriteString(rw, `{"choices":[{"message":{"content":"hi"}}]}`)
	default:
		t.Errorf("unexpected core %s %s", r.Method, r.URL.Path)
		http.NotFound(rw, r)
	}
}

func (w *keyWorld) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := NewRoot("test")
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append(args, "--models-url", w.modelsURL(), "--auth-url", w.srv.URL))
	_, err := captureStdout(root.Execute)
	return out.String(), errb.String(), err
}

// TestModelsEnvCreatesTheModelKey is spec 006's criteria 1 and 2 over
// `models env --raw`.
func TestModelsEnvCreatesTheModelKey(t *testing.T) {
	w := newKeyWorld(t, "")
	out, errOut, err := w.run(t, "models", "env", "--raw")
	if err != nil {
		t.Fatalf("models env --raw: %v %s", err, errOut)
	}
	if strings.TrimSpace(out) != "pat_k1.value" {
		t.Fatalf("printed %q, want the key", out)
	}
	if !strings.Contains(errOut, "created now") {
		t.Fatalf("stderr %q does not say the key is new", errOut)
	}
	if len(w.requests) != 1 {
		t.Fatalf("requests = %v", w.requests)
	}
	req := w.requests[0]
	if _, has := req["org_id"]; has {
		t.Fatalf("the personal context sent org_id: %v", req)
	}
	if req["name"] != "latere-cli on laptop" || req["expires_at"] == nil {
		t.Fatalf("request = %v", req)
	}
	grants, _ := json.Marshal(req["grants"])
	if !strings.Contains(string(grants), `"lux:model.use"`) || strings.Contains(string(grants), "identifier") {
		t.Fatalf("grants = %s, want model.use on every Model", grants)
	}
	// The next call answers the kept key.
	if out, _, err := w.run(t, "models", "env", "--raw"); err != nil || strings.TrimSpace(out) != "pat_k1.value" || len(w.requests) != 1 {
		t.Fatalf("second call = %q %v; %d requests", out, err, len(w.requests))
	}
}

// TestModelKeyFollowsTheContext is spec 006's criterion 2, the context half.
func TestModelKeyFollowsTheContext(t *testing.T) {
	w := newKeyWorld(t, "org-1")
	if _, _, err := w.run(t, "models", "env", "--raw"); err != nil {
		t.Fatal(err)
	}
	if w.requests[0]["org_id"] != "org-1" {
		t.Fatalf("request = %v, want org-1", w.requests[0])
	}
	slots, err := w.store.Slots()
	if err != nil || len(slots) != 1 || !strings.HasSuffix(slots[0], "|u1|org-1") {
		t.Fatalf("slots = %v, %v", slots, err)
	}
}

// TestModelKeyRefusals is spec 006's criterion 3.
func TestModelKeyRefusals(t *testing.T) {
	for status, want := range map[string]string{
		"member_keys_off":  "does not let members create their own keys",
		"key_limit":        "maximum number of keys",
		"pending_approval": "org admin must approve",
	} {
		t.Run(status, func(t *testing.T) {
			w := newKeyWorld(t, "org-1")
			w.status = status
			_, errOut, err := w.run(t, "models", "env", "--raw")
			got := errOut
			if err != nil {
				got = err.Error()
			}
			if !strings.Contains(got, want) {
				t.Fatalf("got %q (err %v), want %q", got, err, want)
			}
		})
	}
}

// TestHandedKeyNeedsNoLogin: a key handed in through LATERE_MODEL_KEY is
// presented as is, with no saved login and no key created, which is how a
// CI job that was handed a key calls models.
func TestHandedKeyNeedsNoLogin(t *testing.T) {
	w := newKeyWorld(t, "")
	t.Setenv("LATERE_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv(modelkey.EnvKey, "pat_ci.value")
	out, errOut, err := w.run(t, "models", "env", "--raw")
	if err != nil || strings.TrimSpace(out) != "pat_ci.value" || len(w.requests) != 0 {
		t.Fatalf("env --raw = %q %v %s; requests %v", out, err, errOut, w.requests)
	}
	if !strings.Contains(errOut, "$LATERE_MODEL_KEY") {
		t.Fatalf("stderr %q does not name the handed key", errOut)
	}
	if out, _, err := w.run(t, "models", "list"); err != nil || !strings.Contains(out, stubModels[0]) {
		t.Fatalf("list = %q %v", out, err)
	}
	for _, c := range w.coreCalls() {
		if c.bearer != "pat_ci.value" {
			t.Fatalf("core call %s presented %q, want the handed key", c.path, c.bearer)
		}
	}
}

// TestInvokeRetriesAFreshKey is spec 006's criterion 6, first half: the core
// does not know a key it has not been told about yet, and the call waits for
// it.
func TestInvokeRetriesAFreshKey(t *testing.T) {
	w := newKeyWorld(t, "")
	w.accept = func(_ string, call int32) bool { return call >= 3 }
	out, errOut, err := w.run(t, "models", "invoke", "--model", "m", "hello")
	if err != nil {
		t.Fatalf("invoke: %v %s", err, errOut)
	}
	if strings.TrimSpace(out) != "hi" || w.calls.Load() != 3 {
		t.Fatalf("out %q after %d calls", out, w.calls.Load())
	}
}

// TestInvokeReplacesARefusedKey is spec 006's criterion 6, second half: a
// key the core refuses long after its creation is revoked and replaced.
func TestInvokeReplacesARefusedKey(t *testing.T) {
	w := newKeyWorld(t, "")
	w.putOldKey(t)
	w.accept = func(bearer string, _ int32) bool { return bearer != "pat_old.value" }
	out, errOut, err := w.run(t, "models", "invoke", "--model", "m", "hello")
	if err != nil {
		t.Fatalf("invoke: %v %s", err, errOut)
	}
	if strings.TrimSpace(out) != "hi" || len(w.revoked) != 1 || w.revoked[0] != "old" || len(w.requests) != 1 {
		t.Fatalf("out %q revoked %v created %d", out, w.revoked, len(w.requests))
	}
}

// putOldKey stores a key created a day ago, which the core should know.
func (w *keyWorld) putOldKey(t *testing.T) {
	t.Helper()
	l := modelkey.Login{AuthBase: w.srv.URL, Sub: "u1"}
	if err := w.store.Put(l.Slot(), modelkey.Record{
		ID: "old", Prefix: "pat_old", Value: "pat_old.value", Context: modelkey.PersonalContext,
		Status: "active", CreatedAt: time.Now().Add(-24 * time.Hour), ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
}

// TestModelsKeyShowAndRevoke is spec 006's criterion 8, first half, over
// `models key`.
func TestModelsKeyShowAndRevoke(t *testing.T) {
	w := newKeyWorld(t, "")
	if out, _, err := w.run(t, "models", "key"); err != nil || !strings.Contains(out, "no model key") {
		t.Fatalf("before = %q %v", out, err)
	}
	if _, _, err := w.run(t, "models", "env", "--raw"); err != nil {
		t.Fatal(err)
	}
	out, _, err := w.run(t, "models", "key")
	if err != nil || !strings.Contains(out, "pat_k1") || !strings.Contains(out, w.store.Path) {
		t.Fatalf("show = %q %v", out, err)
	}
	if out, _, err := w.run(t, "models", "key", "revoke"); err != nil || !strings.Contains(out, "revoked pat_k1") {
		t.Fatalf("revoke = %q %v", out, err)
	}
	if len(w.revoked) != 1 {
		t.Fatalf("revoked at auth = %v", w.revoked)
	}
	if slots, _ := w.store.Slots(); len(slots) != 0 {
		t.Fatalf("slots after revoke = %v", slots)
	}
	if out, _, err := w.run(t, "models", "key", "revoke"); err != nil || !strings.Contains(out, "no model key") {
		t.Fatalf("second revoke = %q %v", out, err)
	}
}

// TestLogoutForgetsTheModelKeys is spec 006's criterion 8, second half.
func TestLogoutForgetsTheModelKeys(t *testing.T) {
	w := newKeyWorld(t, "")
	if _, _, err := w.run(t, "models", "env", "--raw"); err != nil {
		t.Fatal(err)
	}
	root := NewRoot("test")
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"logout", "--auth-url", w.srv.URL})
	if _, err := captureStdout(root.Execute); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if len(w.revoked) != 1 || w.revoked[0] != "k1" {
		t.Fatalf("revoked = %v", w.revoked)
	}
	if slots, _ := w.store.Slots(); len(slots) != 0 {
		t.Fatalf("slots after logout = %v", slots)
	}
}

// TestModelKeySourceWaitsThenAnswersTheAcceptedKey: the bearer source of a
// review or a local agent session has the core accept the key once, waiting
// out a fresh key, and answers it from then on without another call.
func TestModelKeySourceWaitsThenAnswersTheAcceptedKey(t *testing.T) {
	w := newKeyWorld(t, "")
	w.accept = func(_ string, call int32) bool { return call >= 2 }
	src := modelKeySource(w.modelsURL(), w.srv.URL)
	for range 3 {
		key, err := src(t.Context())
		if err != nil || key != "pat_k1.value" {
			t.Fatalf("source = %q, %v", key, err)
		}
	}
	if w.calls.Load() != 2 {
		t.Fatalf("core calls = %d, want the refused list and the accepted one", w.calls.Load())
	}
}

// TestModelKeySourceReportsARefusedHandedKey: a handed key the core refuses
// is reported, not replaced, since the CLI did not create it.
func TestModelKeySourceReportsARefusedHandedKey(t *testing.T) {
	w := newKeyWorld(t, "")
	t.Setenv(modelkey.EnvKey, "pat_ci.value")
	w.accept = func(string, int32) bool { return false }
	if _, err := modelKeySource(w.modelsURL(), w.srv.URL)(t.Context()); err == nil || !strings.Contains(err.Error(), "unauthenticated") {
		t.Fatalf("source err = %v, want the core's refusal", err)
	}
	if len(w.requests) != 0 || w.calls.Load() != 1 {
		t.Fatalf("created %d keys, %d core calls; want none and one", len(w.requests), w.calls.Load())
	}
}
