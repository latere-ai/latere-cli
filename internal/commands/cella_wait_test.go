// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestWaitCommandTimeoutBoundsPolling(t *testing.T) {
	for _, stage := range []string{"headers", "body", "poll_interval"} {
		t.Run(stage, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if stage == "poll_interval" {
					_, _ = io.WriteString(w, `{"phase":"running"}`)
					return
				}
				if stage == "body" {
					_, _ = io.WriteString(w, `{"phase":`)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := &api.Client{BaseURL: server.URL, HTTP: server.Client()}
			done := make(chan error, 1)
			go func() { _, err := waitCommand(ctx, client, "dev", "command", 25*time.Millisecond); done <- err }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "wait timed out") {
					t.Fatalf("wait timeout = %v", err)
				}
			case <-time.After(250 * time.Millisecond):
				t.Error("wait exceeded its timeout")
			}
			cancel()
			if calls.Load() != 1 {
				t.Errorf("poll count = %d, want exactly one request", calls.Load())
			}
		})
	}
}

func TestWaitCommandPreservesCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := waitCommand(ctx, &api.Client{BaseURL: server.URL, HTTP: server.Client()}, "dev", "command", time.Minute)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("poll did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "wait timed out") {
			t.Fatalf("caller cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled wait did not return")
	}
}

func TestWaitCommandCompletedAndFailedRequests(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			if status == http.StatusOK {
				_, _ = io.WriteString(w, `{"phase":"exited","exit_code":7}`)
			} else {
				_, _ = io.WriteString(w, `{"error":{"code":"forbidden","message":"denied"}}`)
			}
		}))
		client := &api.Client{BaseURL: server.URL, HTTP: server.Client()}
		result, err := waitCommand(t.Context(), client, "dev", "command", 0)
		server.Close()
		if status == http.StatusOK {
			if err != nil || result.Phase != "exited" || result.ExitCode == nil || *result.ExitCode != 7 {
				t.Errorf("finished command = %+v, %v", result, err)
			}
		} else {
			var apiErr *api.APIError
			if !errors.As(err, &apiErr) || apiErr.Status != status {
				t.Errorf("API error was not preserved: %v", err)
			}
		}
	}
}

func TestCellaWaitCommandEnforcesTimeout(t *testing.T) {
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), "test-token"))
	requestStopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sandboxes/dev/commands/command" {
			t.Errorf("unexpected wait request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"phase":`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(requestStopped)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	command := newCeWaitCmd()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"dev", "command", "--timeout", "1", "--api-url", server.URL})
	done := make(chan error, 1)
	go func() { done <- command.ExecuteContext(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "wait timed out") {
			t.Fatalf("CLI wait timeout = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("--timeout did not bound the CLI's stalled response")
	}
	select {
	case <-requestStopped:
	case <-time.After(time.Second):
		t.Fatal("timed-out CLI left its HTTP request active")
	}
}
