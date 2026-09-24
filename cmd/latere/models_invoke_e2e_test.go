// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// chatPath is the OpenAI door's Chat Completions path under a models base.
const chatPath = "/openai/v1/chat/completions"

func TestModelsInvokeConfiguredOutputE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess e2e skipped with -short")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const body = `{"choices":[{"message":{"content":"first\nsecond"}}]}`
	for _, format := range []string{"text", "json"} {
		for _, writable := range []string{"1", "0"} {
			t.Run(format+"/writable="+writable, func(t *testing.T) {
				root := t.TempDir()
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != chatPath || r.Header.Get("Authorization") != "Bearer synthetic-key" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, " \n"+body+"\n ")
				}))
				defer server.Close()
				output := filepath.Join(t.TempDir(), "output.json")
				if err := os.WriteFile(output, nil, 0600); err != nil {
					t.Fatal(err)
				}
				// Reuse the helper that installs an inherited writer on the full command tree.
				args := []string{"-test.run=^TestCellaDownloadOutputHelperProcess$", "--", "models", "invoke", "test", "--model", "test-model", "--models-url", server.URL}
				if format == "json" {
					args = append(args, "--json")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = append(modelsEnv(root, "synthetic-key"), "LATERE_TEST_DOWNLOAD_OUTPUT="+output, "LATERE_TEST_DOWNLOAD_WRITABLE="+writable)
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				data, readErr := os.ReadFile(output)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if writable == "1" {
					want := "first\nsecond\n"
					if format == "json" {
						want = body + "\n"
					}
					if err != nil || diagnostic.Len() != 0 || string(data) != want {
						t.Errorf("output=%q error=%v stderr=%q, want %q", data, err, diagnostic.String(), want)
					}
				} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || diagnostic.Len() == 0 || len(data) != 0 {
					t.Errorf("write failure: error=%v stderr=%q output=%q", err, diagnostic.String(), data)
				}
				if out.Len() != 0 {
					t.Errorf("result leaked to process stdout: %q", out.String())
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}

func TestModelsInvokeReportsOutputFailuresE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	const response = `{"choices":[{"message":{"content":"test answer"}}]}`
	for _, format := range []string{"text", "json"} {
		for _, mode := range []string{"writable", "read only"} {
			t.Run(format+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				dest := filepath.Join(root, "answer")
				const before = "existing answer\n"
				if err := os.WriteFile(dest, []byte(before), 0600); err != nil {
					t.Fatal(err)
				}
				flags := os.O_WRONLY | os.O_APPEND
				if mode == "read only" {
					flags = os.O_RDONLY
				}
				file, err := os.OpenFile(dest, flags, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = file.Close() }()
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil || r.URL.Path != chatPath || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" || !bytes.Contains(body, []byte("test prompt")) {
						t.Errorf("unexpected inference request: %s %s (%v)", r.Method, r.URL.Path, err)
					}
					_, _ = w.Write([]byte(response))
				}))
				defer server.Close()
				args := []string{"models", "invoke", "--models-url", server.URL, "--model", "test-model", "test prompt"}
				if format == "json" {
					args = append(args, "--json")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = modelsEnv(root, "test-key")
				var stderr bytes.Buffer
				command.Stdout, command.Stderr = file, &stderr
				err = command.Run()
				want := before
				if mode == "read only" {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "write") {
						t.Errorf("lost answer reported success: %v: %s", err, stderr.String())
					}
				} else {
					if format == "json" {
						want += response + "\n"
					} else {
						want += "test answer\n"
					}
					if err != nil || stderr.Len() != 0 {
						t.Errorf("writable output failed: %v: %s", err, stderr.String())
					}
				}
				if data, err := os.ReadFile(dest); err != nil || string(data) != want {
					t.Errorf("answer file=%q (%v), want %q", data, err, want)
				}
				if calls.Load() != 1 {
					t.Errorf("inference requests=%d, want 1", calls.Load())
				}
			})
		}
	}
}

func TestModelsInvokeRedirectsPreservePromptE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	const prompt = "Keep this prompt intact"
	const reply = `{"choices":[{"message":{"content":"complete answer"}}]}`
	for _, raw := range []bool{false, true} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			mode := "text"
			if raw {
				mode = "json"
			}
			t.Run(mode+"/"+strconv.Itoa(status), func(t *testing.T) {
				root := t.TempDir()
				invalid := status < 307
				var initial, redirected atomic.Int32
				var original atomic.Value
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if r.URL.Path == "/redirected" {
						redirected.Add(1)
						if !invalid && (r.Method != http.MethodPost || string(body) != original.Load() || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer test-key") {
							t.Error("redirect changed inference method, body or headers")
						}
						_, _ = w.Write([]byte(reply))
						return
					}
					initial.Add(1)
					if r.URL.Path != chatPath || r.Method != http.MethodPost || !bytes.Contains(body, []byte(prompt)) {
						t.Errorf("unexpected inference request: %s %s %q", r.Method, r.URL.Path, body)
					}
					original.Store(string(body))
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(status)
				}))
				defer server.Close()
				args := []string{"models", "invoke", "--models-url", server.URL, "--model", "test-model", prompt}
				if raw {
					args = append(args, "--json")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = modelsEnv(root, "test-key")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				wantRedirected := int32(1)
				if invalid {
					wantRedirected = 0
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "redirect changed request method") {
						t.Errorf("method-changing redirect=%v, stderr=%q", err, stderr.String())
					}
					if stdout.Len() != 0 {
						t.Errorf("discarded prompt produced an answer: %q", stdout.String())
					}
				} else {
					want := "complete answer\n"
					if raw {
						want = reply + "\n"
					}
					if err != nil || stdout.String() != want {
						t.Errorf("valid redirect=%v, stdout=%q stderr=%q", err, stdout.String(), stderr.String())
					}
				}
				if initial.Load() != 1 || redirected.Load() != wantRedirected {
					t.Errorf("initial/redirected calls=%d/%d, want 1/%d", initial.Load(), redirected.Load(), wantRedirected)
				}
			})
		}
	}
}

func TestModelsInvokeRejectsIncompleteResponsesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("binary e2e skipped with -short")
	}
	binary := latereBinary(t)
	const reply = `{"choices":[{"message":{"content":"complete answer"}}]}`
	for _, raw := range []bool{false, true} {
		mode := "text"
		if raw {
			mode = "json"
		}
		for _, tc := range []struct {
			name, wantError string
			size            int
		}{
			{name: "complete"},
			{name: "short content length", wantError: "unexpected EOF"},
			{name: "interrupted chunks", wantError: "unexpected EOF"},
			{name: "at limit", size: 8 << 20},
			{name: "over limit", size: (8 << 20) + 1, wantError: "exceeds 8 MiB"},
			{name: "provider error", wantError: "provider_unavailable"},
			{name: "provider error over limit", size: (8 << 20) + 1, wantError: "provider_unavailable"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				payload := reply
				if strings.HasPrefix(tc.name, "provider error") {
					payload = `{"error":{"code":"provider_unavailable","message":"No provider for this model is available right now."}}`
				}
				if tc.size > len(payload) {
					// JSON permits trailing whitespace. Truncating it previously
					// hid the size violation even in parsed-text mode.
					payload += strings.Repeat(" ", tc.size-len(payload))
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.Method != http.MethodPost || r.URL.Path != chatPath || r.Header.Get("Authorization") != "Bearer test-key" {
						t.Errorf("unexpected inference request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if strings.HasPrefix(tc.name, "provider error") {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = w.Write([]byte(payload))
						return
					}
					if tc.name == "short content length" {
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)+10))
					}
					_, _ = w.Write([]byte(payload))
					if tc.name == "interrupted chunks" {
						w.(http.Flusher).Flush()
						panic(http.ErrAbortHandler)
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				args := []string{"models", "invoke", "--models-url", server.URL, "--model", "test-model", "Say hello"}
				if raw {
					args = append(args, "--json")
				}
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = modelsEnv(root, "test-key")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				if tc.wantError != "" {
					if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
						t.Errorf("invalid response exit = %v; stderr: %s", err, stderr.String())
					}
					if !strings.Contains(stderr.String(), tc.wantError) {
						t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantError)
					}
					if stdout.Len() != 0 {
						t.Errorf("invalid response printed %d bytes to stdout", stdout.Len())
					}
					return
				}
				want := "complete answer\n"
				if raw {
					want = reply + "\n"
				}
				if err != nil || stdout.String() != want {
					t.Errorf("complete response = %v, stdout bytes=%d; stderr: %s", err, stdout.Len(), stderr.String())
				}
			})
		}
	}
}
