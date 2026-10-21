// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// e2eBinary is the latere binary the end-to-end tests run as a subprocess.
// Every such test runs the same program, so it is linked once per test
// process instead of once per test: a link takes seconds on a loaded machine,
// and one per test adds minutes to the package. On macOS the first run of a
// newly written executable is also slow, because the system assesses the file
// before it executes; with one binary that cost is paid once, in
// latereBinary, and not inside a test's subprocess deadline.
var e2eBinary struct {
	once sync.Once
	dir  string
	path string
	err  error
}

func TestMain(m *testing.M) {
	// The helper-process tests run this test binary again as the CLI. Under
	// -race, each child that exits 0 first sleeps for the detector's
	// atexit_sleep_ms, 1000 by default, a second per subtest. The sleep gives
	// goroutines still running at exit a last chance to race; a helper runs
	// one command to completion and exits, so its children skip it. A GORACE
	// that already sets the option is kept.
	if gorace := os.Getenv("GORACE"); !strings.Contains(gorace, "atexit_sleep_ms") {
		if err := os.Setenv("GORACE", strings.TrimSpace(gorace+" atexit_sleep_ms=0")); err != nil {
			fmt.Fprintf(os.Stderr, "set GORACE: %v\n", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if e2eBinary.dir != "" {
		if err := os.RemoveAll(e2eBinary.dir); err != nil {
			fmt.Fprintf(os.Stderr, "remove the end-to-end binary: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

// latereBinary returns the path of the latere binary built from this package,
// building it on the first call. The binary is shared by every test in the
// process, so a test must not modify or replace it. Its directory holds only
// the binary, so a test can put that directory on PATH.
func latereBinary(t *testing.T) string {
	t.Helper()
	e2eBinary.once.Do(func() {
		dir, err := os.MkdirTemp("", "latere-e2e-")
		if err != nil {
			e2eBinary.err = err
			return
		}
		e2eBinary.dir = dir
		path := filepath.Join(dir, "latere")
		if out, err := exec.Command("go", "build", "-o", path, ".").CombinedOutput(); err != nil {
			e2eBinary.err = fmt.Errorf("build: %w\n%s", err, out)
			return
		}
		// Run it once without a deadline, so the first-run cost of a new
		// executable lands here rather than in the first test to use it.
		if out, err := exec.Command(path, "--version").CombinedOutput(); err != nil {
			e2eBinary.err = fmt.Errorf("run the built binary: %w\n%s", err, out)
			return
		}
		e2eBinary.path = path
	})
	if e2eBinary.err != nil {
		t.Fatal(e2eBinary.err)
	}
	return e2eBinary.path
}
