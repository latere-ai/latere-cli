// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"latere.ai/x/topos/sandbox"
	"latere.ai/x/topos/sandbox/rpc"
)

type tunnelWriteSignalConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *tunnelWriteSignalConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(b)
}

func TestSandboxTunnelCancelDuringSetup(t *testing.T) {
	c, peer := net.Pipe()
	defer c.Close()
	defer peer.Close()
	conn := &tunnelWriteSignalConn{Conn: c, started: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- serveSandboxTunnel(ctx, conn, root, nil, io.Discard) }()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("tunnel did not start setup")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("setup cancellation = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("tunnel setup ignored cancellation")
	}
}

func TestSandboxTunnelCancelIdleOverTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	// Bound the test handshake separately from the cancellation being tested.
	if err := peer.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	session, err := yamux.Server(peer, sandboxYamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- serveSandboxTunnel(ctx, conn, root, nil, io.Discard) }()
	control, err := session.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	var desc SandboxDescriptor
	if err := json.NewDecoder(control).Decode(&desc); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("idle cancellation = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("idle tunnel ignored cancellation")
	}
	select {
	case <-session.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("canceled tunnel left peer connection open")
	}
}

func TestSandboxTunnelWaitsForAcceptedWork(t *testing.T) {
	for _, shutdown := range []string{"cancel", "disconnect"} {
		t.Run(shutdown, func(t *testing.T) {
			conn, peer := net.Pipe()
			defer conn.Close()
			defer peer.Close()
			session, err := yamux.Server(peer, sandboxYamuxConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			consent := func(ctx context.Context, _ string, _ sandbox.ExecOptions) error {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
				return ctx.Err()
			}
			root := t.TempDir()
			done := make(chan error, 1)
			go func() { done <- serveSandboxTunnel(ctx, conn, root, consent, io.Discard) }()
			control, err := session.AcceptStream()
			if err != nil {
				t.Fatal(err)
			}
			var desc SandboxDescriptor
			if err := json.NewDecoder(control).Decode(&desc); err != nil {
				t.Fatal(err)
			}
			stream, err := session.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			provider := rpc.NewClient(stream)
			result := make(chan error, 1)
			go func() {
				_, err := provider.Exec(t.Context(), "local", sandbox.ExecOptions{Argv: []string{"unused"}})
				result <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("work stream did not reach consent")
			}
			if shutdown == "cancel" {
				cancel()
			} else {
				_ = session.Close()
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("accepted work was not canceled")
			}
			select {
			case err := <-done:
				t.Fatalf("tunnel returned before work cleanup finished: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			unblock()
			select {
			case err := <-done:
				if shutdown == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation = %v", err)
				}
				if shutdown == "disconnect" && ctx.Err() != nil {
					t.Fatal("peer disconnect canceled caller context")
				}
			case <-time.After(time.Second):
				t.Fatal("tunnel did not return after work stopped")
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("aborted command succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("RPC caller remained blocked")
			}
		})
	}
}

func TestRunServeSandboxCancellationOverWSS(t *testing.T) {
	t.Setenv("TOPOS_TOKEN", "test-token")
	ready := make(chan error, 1)
	peerStopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(peerStopped)
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{sandboxTunnelSubprotocol}})
		if err != nil {
			ready <- err
			return
		}
		defer ws.CloseNow()
		session, err := yamux.Server(websocket.NetConn(r.Context(), ws, websocket.MessageBinary), sandboxYamuxConfig())
		if err != nil {
			ready <- err
			return
		}
		defer session.Close()
		control, err := session.AcceptStream()
		if err != nil {
			ready <- err
			return
		}
		var desc SandboxDescriptor
		if err := json.NewDecoder(control).Decode(&desc); err != nil {
			ready <- err
			return
		}
		ready <- nil
		<-session.CloseChan()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- runServeSandbox(ctx, server.URL, root, nil) }()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("production WebSocket tunnel did not connect")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WebSocket cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WebSocket tunnel did not stop on cancellation")
	}
	select {
	case <-peerStopped:
	case <-time.After(time.Second):
		t.Fatal("WebSocket peer remained connected")
	}
}
