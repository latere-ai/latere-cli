// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestCellaConfiguredInput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, dir, "synthetic-token"))
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	// Two zero tar blocks form an empty archive; write accepts arbitrary bytes.
	want := make([]byte, 1024)
	input := filepath.Join(dir, "payload.tar")
	if err := os.WriteFile(input, want, 0600); err != nil {
		t.Fatal(err)
	}
	processInput, err := os.CreateTemp(dir, "process-input")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = processInput
	t.Cleanup(func() { os.Stdin = previous; _ = processInput.Close() })
	for _, prefix := range []string{"cella", "sandbox"} {
		for _, mode := range []string{"write", "import"} {
			for _, source := range []string{"default", "dash", "file"} {
				t.Run(prefix+"/"+mode+"/"+source, func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						var got []byte
						if mode == "write" {
							if r.Method != http.MethodPut || r.URL.Path != "/v1/sandboxes/dev/files" {
								t.Errorf("request=%s %s", r.Method, r.URL)
							}
							var body struct{ Path, Content string }
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
							}
							if body.Path != "/workspace/file" {
								t.Errorf("path=%q", body.Path)
							}
							var err error
							got, err = base64.StdEncoding.DecodeString(body.Content)
							if err != nil {
								t.Error(err)
							}
							w.WriteHeader(http.StatusNoContent)
						} else {
							if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/dev/files/import" {
								t.Errorf("request=%s %s", r.Method, r.URL)
							}
							if err := r.ParseMultipartForm(1 << 20); err != nil {
								t.Error(err)
								w.WriteHeader(400)
								return
							}
							defer r.MultipartForm.RemoveAll()
							file, header, err := r.FormFile("tarball")
							if err != nil {
								t.Error(err)
								w.WriteHeader(400)
								return
							}
							defer file.Close()
							got, err = io.ReadAll(file)
							if err != nil {
								t.Error(err)
							}
							_ = json.NewEncoder(w).Encode(map[string]any{"imported": header.Filename, "bytes": len(got)})
						}
						if !bytes.Equal(got, want) {
							t.Errorf("used wrong input: got %d bytes, want %d", len(got), len(want))
						}
					}))
					defer server.Close()
					root := NewRoot("test")
					root.SetIn(bytes.NewReader(want))
					root.SetOut(io.Discard)
					root.SetErr(io.Discard)
					args := []string{prefix, mode, "dev"}
					if mode == "write" {
						args = append(args, "/workspace/file")
					}
					args = append(args, "--api-url", server.URL)
					switch source {
					case "dash":
						args = append(args, "--input", "-")
					case "file":
						args = append(args, "--input", input)
						reader, writer := io.Pipe()
						_ = writer.CloseWithError(errors.New("configured stdin must not be read for a named file"))
						defer reader.Close()
						root.SetIn(reader)
					}
					root.SetArgs(args)
					if _, err := captureStdout(root.Execute); err != nil {
						t.Errorf("command: %v", err)
					}
					if requests.Load() != 1 {
						t.Errorf("requests=%d", requests.Load())
					}
				})
			}
		}
	}
}

func TestCellaWriteConfiguredInputError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, dir, "synthetic-token"))
	t.Setenv("LATERE_NO_UPDATE_CHECK", "1")
	empty, err := os.CreateTemp(dir, "process-input")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = empty
	t.Cleanup(func() { os.Stdin = previous; _ = empty.Close() })
	sentinel := errors.New("configured input failed")
	reader, writer := io.Pipe()
	_ = writer.CloseWithError(sentinel)
	defer reader.Close()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	root := NewRoot("test")
	root.SetIn(io.MultiReader(bytes.NewBufferString("partial"), reader))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"cella", "write", "dev", "/workspace/file", "--api-url", server.URL})
	if err := root.Execute(); !errors.Is(err, sentinel) {
		t.Errorf("lost input failure: %v", err)
	}
	if requests.Load() != 0 {
		t.Errorf("failed input made %d remote writes", requests.Load())
	}
}
