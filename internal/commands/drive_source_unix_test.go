//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestDrivePutRejectsNamedPipeWithoutBlocking(t *testing.T) {
	source := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(source, 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"path":"files/destination","size":0}`)
	}))
	defer server.Close()
	done := make(chan error, 1)
	go func() { _, _, err := execDrive(t, server, "put", source, "files/destination"); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("FIFO upload = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Error("upload blocked opening a named pipe")
		// Release the old implementation's blocking open so the regression test
		// itself always cleans up; no writer is needed by the fixed implementation.
		fd, err := syscall.Open(source, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		_ = syscall.Close(fd)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("blocked upload did not finish after cleanup")
		}
	}
	if requests.Load() != 0 {
		t.Errorf("FIFO upload sent %d HTTP requests", requests.Load())
	}
}
