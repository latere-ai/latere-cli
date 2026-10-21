// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bufio"
	"context"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestForwardPorts(t *testing.T) {
	for _, tc := range []struct {
		arg           string
		local, remote int
		refusal       string
	}{
		{arg: "8080:80", local: 8080, remote: 80},
		{arg: "0:5432", local: 0, remote: 5432},
		{arg: "5432", refusal: "takes <local>:<port>"},
		{arg: "x:5432", refusal: "local port"},
		{arg: "70000:80", refusal: "local port"},
		{arg: "0:0", refusal: "not between 1 and 65535"},
		{arg: "0:70000", refusal: "not between 1 and 65535"},
	} {
		local, remote, err := forwardPorts(tc.arg)
		if tc.refusal != "" {
			if err == nil || !strings.Contains(err.Error(), tc.refusal) {
				t.Errorf("forwardPorts(%q) = %v, want a refusal naming %q", tc.arg, err, tc.refusal)
			}
			continue
		}
		if err != nil || local != tc.local || remote != tc.remote {
			t.Errorf("forwardPorts(%q) = %d, %d, %v; want %d, %d", tc.arg, local, remote, err, tc.local, tc.remote)
		}
	}
}

// TestPortForwardRefusesAnUndeclaredPort: Cella reaches only a port the
// manifest declares and says so only after the socket opens, so the command
// reads the declaration first and never listens for a port it cannot reach.
func TestPortForwardRefusesAnUndeclaredPort(t *testing.T) {
	f := newFakeCore(t)
	f.ports = []int{3000}
	stdout, _, err := f.runCella("", "port-forward", "dev", "0:8080")
	if err == nil || !strings.Contains(err.Error(), "port 8080 is not declared") {
		t.Fatalf("err = %v, want the undeclared port named", err)
	}
	if stdout != "" {
		t.Errorf("a refused port-forward printed %q", stdout)
	}
	if got := f.seen(); !slices.Equal(got, []string{"GET /sandboxes/dev"}) {
		t.Errorf("requests = %v, want the read alone", got)
	}
}

// TestPortForwardCarriesConnections: every connection to the local listener
// is carried both ways over a dial socket of its own, and the command ends
// without an error when it is interrupted.
func TestPortForwardCarriesConnections(t *testing.T) {
	f := newFakeCore(t)
	f.ports = []int{8080}
	c, err := cellaClient(f.url())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out, printed := io.Pipe()
	exited := make(chan error, 1)
	go func() {
		exited <- portForward(ctx, c, "dev", 0, 8080, printed, io.Discard)
		_ = printed.Close()
	}()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("no listening line: %v", err)
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "Forwarding" {
		t.Fatalf("printed %q", line)
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()
	for _, message := range []string{"first connection", "second connection"} {
		conn, err := net.DialTimeout("tcp", fields[1], 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, message); err != nil {
			t.Fatal(err)
		}
		back := make([]byte, len(message))
		if _, err := io.ReadFull(conn, back); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(back) != message {
			t.Errorf("carried back %q, want %q", back, message)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("an interrupted port-forward returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("port-forward did not end with its context")
	}
	dials := 0
	for _, r := range f.seen() {
		if r == "GET /sandboxes/dev/dial/8080" {
			dials++
		}
	}
	// The probe, then one socket per connection.
	if dials != 3 {
		t.Errorf("%d dial sockets in %v, want 3", dials, f.seen())
	}
}
