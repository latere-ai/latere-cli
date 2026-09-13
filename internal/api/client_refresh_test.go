// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// productStub answers only for one bearer and counts what it received. It
// stands for a product enforcing its audience: a lapsed actor token is a
// 401, which is what the re-mint hook exists for.
type productStub struct {
	accepts atomic.Value // string
	calls   atomic.Int32
}

func newProductStub(t *testing.T, accepts string) (*productStub, string) {
	t.Helper()
	p := &productStub{}
	p.accepts.Store(accepts)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.calls.Add(1)
		want, _ := p.accepts.Load().(string)
		if r.Header.Get("Authorization") != "Bearer "+want {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized","message":"token expired"}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	return p, srv.URL
}

func TestClientRemintsOn401AndRetriesOnce(t *testing.T) {
	p, url := newProductStub(t, "fresh-actor")
	mints := 0
	c := NewClient(url)
	c.SetBearer("lapsed-actor", time.Time{})
	c.Refresh = func(context.Context) (string, time.Time, bool) {
		mints++
		return "fresh-actor", time.Time{}, true
	}

	var out any
	if err := c.GetJSON(t.Context(), "/v1/sandboxes", &out); err != nil {
		t.Fatalf("GetJSON after re-mint: %v", err)
	}
	if got := p.calls.Load(); got != 2 {
		t.Errorf("product calls = %d, want 2 (401 then retried)", got)
	}
	if mints != 1 {
		t.Errorf("mints = %d, want exactly one per request", mints)
	}
}

// A product token lives five minutes; `cella wait` and `cella logs`
// follow a job for longer, on one client. Every lapse must be replaced,
// not only the first: a once-per-client hook leaves the command holding
// a dead token for the rest of its run.
func TestClientRemintsAtEveryLapse(t *testing.T) {
	p, url := newProductStub(t, "actor-1")
	mints := 0
	c := NewClient(url)
	c.SetBearer("actor-1", time.Now().Add(2*time.Hour))
	c.Refresh = func(context.Context) (string, time.Time, bool) {
		mints++
		token := fmt.Sprintf("actor-%d", mints+1)
		p.accepts.Store(token)
		return token, time.Now().Add(2 * time.Hour), true
	}

	for lapse := range 3 {
		// The held token is now due; the next request replaces it before
		// sending, so the product never sees a lapsed one.
		c.expiresAt = time.Now().Add(time.Second)
		var out any
		if err := c.GetJSON(t.Context(), "/v1/sandboxes", &out); err != nil {
			t.Fatalf("lapse %d: %v", lapse, err)
		}
		if mints != lapse+1 {
			t.Fatalf("lapse %d: mints = %d, want %d", lapse, mints, lapse+1)
		}
	}
	if got := p.calls.Load(); got != 3 {
		t.Errorf("product calls = %d, want 3 (no refusal reached the product)", got)
	}
}

// A 401 after a proactive re-mint is still retried. The proactive path
// used to latch the hook off, which left the second lapse of a long
// command unrecoverable.
func TestClientRemintsOn401AfterAProactiveRemint(t *testing.T) {
	p, url := newProductStub(t, "actor-3")
	mints := 0
	c := NewClient(url)
	c.SetBearer("actor-1", time.Now().Add(time.Second))
	c.Refresh = func(context.Context) (string, time.Time, bool) {
		mints++
		// The proactive mint hands back a token the product has already
		// retired; only the reactive one is accepted.
		return fmt.Sprintf("actor-%d", mints+1), time.Now().Add(2 * time.Hour), true
	}

	var out any
	if err := c.GetJSON(t.Context(), "/v1/sandboxes", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if mints != 2 {
		t.Errorf("mints = %d, want 2 (proactive, then after the 401)", mints)
	}
	if got := p.calls.Load(); got != 2 {
		t.Errorf("product calls = %d, want 2", got)
	}
}

// An actor token lives five minutes, so a command that starts near the
// end of one re-mints before it sends rather than after a refusal.
func TestClientRemintsBeforeAKnownExpiry(t *testing.T) {
	p, url := newProductStub(t, "fresh-actor")
	c := NewClient(url)
	c.SetBearer("lapsing-actor", time.Now().Add(10*time.Second))
	c.Refresh = func(context.Context) (string, time.Time, bool) { return "fresh-actor", time.Time{}, true }

	var out any
	if err := c.GetJSON(t.Context(), "/v1/sandboxes", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if got := p.calls.Load(); got != 1 {
		t.Errorf("product calls = %d, want 1 (re-minted before sending)", got)
	}
}

func TestClientKeepsTheRefusalWhenTheMintFails(t *testing.T) {
	_, url := newProductStub(t, "something-else")
	c := NewClient(url)
	c.SetBearer("lapsed-actor", time.Time{})
	c.Refresh = func(context.Context) (string, time.Time, bool) { return "", time.Time{}, false }

	var out any
	err := c.GetJSON(t.Context(), "/v1/sandboxes", &out)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want the product's original 401", err)
	}
}

func TestClientDoesNotRetryUnseekableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized","message":"nope"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetBearer("lapsed-actor", time.Time{})
	c.Refresh = func(context.Context) (string, time.Time, bool) { return "fresh-actor", time.Time{}, true }
	body := io.LimitReader(strings.NewReader(`{"x":1}`), 7) // not an io.Seeker
	err := c.Do(t.Context(), http.MethodPost, "/v1/things", body, "application/json", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want the original 401 (unseekable body must not retry)", err)
	}
}
