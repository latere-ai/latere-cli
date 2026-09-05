// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// Exercise the real commands, including their multipart producer goroutines.
// A malformed URL fails before net/http takes ownership of the request body;
// the command must close its own pipe so the producer can release its file.
func TestCellaMultipartRequestFailureStopsProducer(t *testing.T) {
	source := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(source, []byte("upload content"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LATERE_TOKEN_FILE", writeTokenFile(t, t.TempDir(), "test-token"))
	for _, name := range []string{"upload", "import"} {
		for _, dest := range []bool{false, true} {
			label := name + "/file"
			if dest {
				label = name + "/destination"
			}
			t.Run(label, func(t *testing.T) {
				cmd := newCeUploadCmd()
				args := []string{"dev", source, "--api-url", "http://["}
				if name == "import" {
					cmd = newCeImportCmd()
					args = []string{"dev", "--input", source, "--api-url", "http://["}
				}
				if dest {
					args = append(args, "--dest", "/workspace")
				}
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetErr(&bytes.Buffer{})
				cmd.SetArgs(args)
				if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "missing ']' in host") {
					t.Fatalf("expected malformed API URL error, got %v", err)
				}
				// Stack inspection targets only these command producers, avoiding
				// flaky assertions about unrelated HTTP or telemetry goroutines.
				deadline := time.Now().Add(time.Second)
				for {
					var dump bytes.Buffer
					if err := pprof.Lookup("goroutine").WriteTo(&dump, 2); err != nil {
						t.Fatal(err)
					}
					var workers []string
					for stack := range strings.SplitSeq(dump.String(), "\n\n") {
						if strings.Contains(stack, "commands.newCeUploadCmd.func") || strings.Contains(stack, "commands.newCeImportCmd.func") {
							workers = append(workers, stack)
						}
					}
					if len(workers) == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("command left multipart producers running after returning: %s", strings.Join(workers, "\n\n"))
					}
					time.Sleep(5 * time.Millisecond)
				}
			})
		}
	}
}
