// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
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

// The model key (specs/006-model-key.md) against a stub that is both auth's
// /me/keys and the Lux core behind the origin at /v1/models.

type keyWorld struct {
	srv      *httptest.Server
	store    modelkey.File
	mu       sync.Mutex
	requests []map[string]any
	revoked  []string
	// status is what POST /me/keys answers: "active", "pending_approval",
	// or an auth error code.
	status string
	// accept decides whether the core accepts a bearer on this call.
	accept func(bearer string, call int32) bool
	calls  atomic.Int32
	n      int
}

func newKeyWorld(t *testing.T, orgID string) *keyWorld {
	t.Helper()
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	t.Setenv(modelkey.EnvKey, "")
	t.Setenv("LATERE_LUX_TOKEN", "")
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1/models/openai/v1/chat/completions":
			n := w.calls.Add(1)
			bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !w.accept(bearer, n) {
				rw.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(rw, `{"error":{"code":"unauthenticated","message":"no key"}}`)
				return
			}
			_, _ = io.WriteString(rw, `{"choices":[{"message":{"content":"hi"}}]}`)
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

func (w *keyWorld) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := NewRoot("test")
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append(args, "--lux-url", w.srv.URL+"/v1/models", "--auth-url", w.srv.URL))
	_, err := captureStdout(root.Execute)
	return out.String(), errb.String(), err
}

// TestLuxTokenCreatesTheModelKey is criteria 1 and 2.
func TestLuxTokenCreatesTheModelKey(t *testing.T) {
	w := newKeyWorld(t, "")
	out, errOut, err := w.run(t, "lux", "env", "--raw")
	if err != nil {
		t.Fatalf("lux env --raw: %v %s", err, errOut)
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
	if out, _, err := w.run(t, "lux", "env", "--raw"); err != nil || strings.TrimSpace(out) != "pat_k1.value" || len(w.requests) != 1 {
		t.Fatalf("second call = %q %v; %d requests", out, err, len(w.requests))
	}
}

// TestModelKeyFollowsTheContext is criterion 2's context half.
func TestModelKeyFollowsTheContext(t *testing.T) {
	w := newKeyWorld(t, "org-1")
	if _, _, err := w.run(t, "lux", "env", "--raw"); err != nil {
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

// TestModelKeyRefusals is criterion 3.
func TestModelKeyRefusals(t *testing.T) {
	for status, want := range map[string]string{
		"member_keys_off":  "does not let members create their own keys",
		"key_limit":        "maximum number of keys",
		"pending_approval": "org admin must approve",
	} {
		t.Run(status, func(t *testing.T) {
			w := newKeyWorld(t, "org-1")
			w.status = status
			_, errOut, err := w.run(t, "lux", "env", "--raw")
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

// TestPassthroughTokenSkipsTheKey is criterion 4 for the core: a handed-in
// bearer is presented as is, and no key is created.
func TestPassthroughTokenSkipsTheKey(t *testing.T) {
	w := newKeyWorld(t, "")
	out, _, err := w.run(t, "lux", "env", "--raw", "--token", "handed-in")
	if err != nil || strings.TrimSpace(out) != "handed-in" || len(w.requests) != 0 {
		t.Fatalf("out %q err %v requests %v", out, err, w.requests)
	}
}

// TestInvokeRetriesAFreshKey is criterion 6's first half: the core does not
// know a key it has not been told about yet, and the call waits for it.
func TestInvokeRetriesAFreshKey(t *testing.T) {
	w := newKeyWorld(t, "")
	w.accept = func(_ string, call int32) bool { return call >= 3 }
	out, errOut, err := w.run(t, "lux", "invoke", "--model", "m", "hello")
	if err != nil {
		t.Fatalf("invoke: %v %s", err, errOut)
	}
	if strings.TrimSpace(out) != "hi" || w.calls.Load() != 3 {
		t.Fatalf("out %q after %d calls", out, w.calls.Load())
	}
}

// TestInvokeReplacesARefusedKey is criterion 6's second half: a key the core
// refuses long after its creation is revoked and replaced.
func TestInvokeReplacesARefusedKey(t *testing.T) {
	w := newKeyWorld(t, "")
	l := modelkey.Login{AuthBase: w.srv.URL, Sub: "u1"}
	if err := w.store.Put(l.Slot(), modelkey.Record{
		ID: "old", Prefix: "pat_old", Value: "pat_old.value", Context: modelkey.PersonalContext,
		Status: "active", CreatedAt: time.Now().Add(-24 * time.Hour), ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	w.accept = func(bearer string, _ int32) bool { return bearer != "pat_old.value" }
	out, errOut, err := w.run(t, "lux", "invoke", "--model", "m", "hello")
	if err != nil {
		t.Fatalf("invoke: %v %s", err, errOut)
	}
	if strings.TrimSpace(out) != "hi" || len(w.revoked) != 1 || w.revoked[0] != "old" || len(w.requests) != 1 {
		t.Fatalf("out %q revoked %v created %d", out, w.revoked, len(w.requests))
	}
}

// TestLuxKeyShowAndRevoke is criterion 8's first half.
func TestLuxKeyShowAndRevoke(t *testing.T) {
	w := newKeyWorld(t, "")
	if out, _, err := w.run(t, "lux", "key"); err != nil || !strings.Contains(out, "no model key") {
		t.Fatalf("before = %q %v", out, err)
	}
	if _, _, err := w.run(t, "lux", "env", "--raw"); err != nil {
		t.Fatal(err)
	}
	out, _, err := w.run(t, "lux", "key")
	if err != nil || !strings.Contains(out, "pat_k1") || !strings.Contains(out, w.store.Path) {
		t.Fatalf("show = %q %v", out, err)
	}
	if out, _, err := w.run(t, "lux", "key", "revoke"); err != nil || !strings.Contains(out, "revoked pat_k1") {
		t.Fatalf("revoke = %q %v", out, err)
	}
	if len(w.revoked) != 1 {
		t.Fatalf("revoked at auth = %v", w.revoked)
	}
	if slots, _ := w.store.Slots(); len(slots) != 0 {
		t.Fatalf("slots after revoke = %v", slots)
	}
}

// TestLogoutForgetsTheModelKeys is criterion 8's second half.
func TestLogoutForgetsTheModelKeys(t *testing.T) {
	w := newKeyWorld(t, "")
	if _, _, err := w.run(t, "lux", "env", "--raw"); err != nil {
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
