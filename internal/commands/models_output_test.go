// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/latere-ai/latere-cli/internal/modelkey"
)

// TestModelsInvokeConfiguredOutput: the reply goes to the command's writer,
// never the process's stdout, and a write that fails is the command's error.
func TestModelsInvokeConfiguredOutput(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	t.Setenv(modelkey.EnvKey, "synthetic-key")
	const body = `{"choices":[{"message":{"content":"first\nsecond"}}]}`
	for _, jsonF := range []bool{false, true} {
		for _, failAfter := range []int{-1, 0, 3} {
			t.Run(fmt.Sprintf("json=%t/failAfter=%d", jsonF, failAfter), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/openai/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					_, _ = io.WriteString(w, " \n"+body+"\n ")
				}))
				defer server.Close()
				out := &evalOutputWriter{}
				sentinel := errors.New("invoke output unavailable")
				if failAfter >= 0 {
					out.remaining, out.err = failAfter, sentinel
				}
				root := NewRoot("test")
				root.SetOut(out)
				root.SetErr(io.Discard)
				args := []string{"models", "invoke", "test", "--model", "test-model", "--models-url", server.URL}
				if jsonF {
					args = append(args, "--json")
				}
				root.SetArgs(args)
				leaked, err := captureStdout(root.Execute)
				want := "first\nsecond\n"
				if jsonF {
					want = body + "\n"
				}
				if failAfter >= 0 {
					want = want[:failAfter]
					if !errors.Is(err, sentinel) {
						t.Errorf("output error=%v", err)
					}
				} else if err != nil {
					t.Errorf("invoke output: %v", err)
				}
				if out.String() != want {
					t.Errorf("output=%q, want %q", out.String(), want)
				}
				if leaked != "" {
					t.Errorf("result leaked to process stdout: %q", leaked)
				}
				if requests.Load() != 1 {
					t.Errorf("requests=%d, want 1", requests.Load())
				}
			})
		}
	}
}

// TestModelsListConfiguredOutput: the list goes to the command's writer, and
// a write that fails is the command's error.
func TestModelsListConfiguredOutput(t *testing.T) {
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	t.Setenv(modelkey.EnvKey, "synthetic-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/openai/v1/models" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Errorf("request=%s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"a/one"},{"id":"b/two"}]}`)
	}))
	defer server.Close()
	for _, failAfter := range []int{-1, 0, 3} {
		out := &evalOutputWriter{}
		sentinel := errors.New("list output unavailable")
		if failAfter >= 0 {
			out.remaining, out.err = failAfter, sentinel
		}
		root := NewRoot("test")
		root.SetOut(out)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"models", "list", "--models-url", server.URL})
		leaked, err := captureStdout(root.Execute)
		want := "a/one\nb/two\n"
		if failAfter >= 0 {
			want = want[:failAfter]
			if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "write model list") {
				t.Errorf("failAfter=%d: error=%v", failAfter, err)
			}
		} else if err != nil {
			t.Errorf("list output: %v", err)
		}
		if out.String() != want || leaked != "" {
			t.Errorf("failAfter=%d: output=%q leaked=%q, want %q", failAfter, out.String(), leaked, want)
		}
	}
}
