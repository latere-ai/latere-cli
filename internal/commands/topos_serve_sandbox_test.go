// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"latere.ai/x/topos/sandbox"
	"latere.ai/x/topos/sandbox/rpc"
)

func TestToWSURL(t *testing.T) {
	cases := map[string]string{
		"https://topos.latere.ai": "wss://topos.latere.ai",
		"http://localhost:8080":   "ws://localhost:8080",
		"wss://already":           "wss://already",
	}
	for in, want := range cases {
		if got := toWSURL(in); got != want {
			t.Errorf("toWSURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToposTunnelBearerDevOverride(t *testing.T) {
	t.Setenv("TOPOS_TOKEN", "dev-token")
	got, err := toposTunnelBearer()
	if err != nil || got != "dev-token" {
		t.Fatalf("toposTunnelBearer with TOPOS_TOKEN = (%q, %v), want dev-token", got, err)
	}
}

type tunnelOutcome struct {
	auth string
	node string
	body string
}

// standInToposd is the sandboxtunnel.Server counterpart for the client tests: it
// accepts the WSS upgrade, runs yamux.Server, reads the descriptor, drives one
// ReadFile back over the tunnel, and reports what it saw on got.
func standInToposd(t *testing.T, got chan tunnelOutcome) *httptest.Server {
	t.Helper()
	handler := func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{sandboxTunnelSubprotocol},
		})
		if err != nil {
			return
		}
		defer c.CloseNow() //nolint:errcheck
		c.SetReadLimit(-1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nc := websocket.NetConn(ctx, c, websocket.MessageBinary)
		sess, err := yamux.Server(nc, yamux.DefaultConfig())
		if err != nil {
			return
		}
		ctrl, err := sess.AcceptStream()
		if err != nil {
			return
		}
		line, err := bufio.NewReader(ctrl).ReadBytes('\n')
		if err != nil {
			return
		}
		var desc struct {
			NodeID string `json:"node_id"`
			Root   string `json:"root"`
		}
		_ = json.Unmarshal(line, &desc)

		stream, err := sess.OpenStream()
		if err != nil {
			return
		}
		provider := rpc.NewClient(stream)
		sb, err := provider.Create(ctx, sandbox.CreateOptions{})
		if err != nil {
			return
		}
		b, _ := provider.ReadFile(ctx, sb.ID, "hello.txt")
		got <- tunnelOutcome{auth: r.Header.Get("Authorization"), node: desc.NodeID, body: string(b)}
	}
	hs := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(hs.Close)
	return hs
}

// TestServeSandboxDialsWSSAndServes is the client-side real-WSS e2e: the
// serve-sandbox command's dial path connects to a stand-in toposd over a real
// websocket, presents its Bearer, and serves the workspace — and the server side
// drives the laptop's real files back over the tunnel.
func TestServeSandboxDialsWSSAndServes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi from laptop"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := make(chan tunnelOutcome, 1)
	hs := standInToposd(t, got)

	t.Setenv("TOPOS_TOKEN", "dev-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	allow := func(context.Context, string, sandbox.ExecOptions) error { return nil }
	go func() { _ = runServeSandbox(ctx, hs.URL, root, allow) }()

	select {
	case o := <-got:
		if o.auth != "Bearer dev-token" {
			t.Errorf("Authorization = %q, want 'Bearer dev-token'", o.auth)
		}
		if o.node == "" {
			t.Errorf("descriptor node_id was empty")
		}
		if o.body != "hi from laptop" {
			t.Errorf("driven ReadFile = %q, want 'hi from laptop'", o.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: the server never drove the laptop over WSS")
	}
}

// TestServeSandboxCommandExecute drives the whole cobra command — RunE, the
// --yes allow-all consent swap, --root resolution, and the dial — proving the
// wired command connects and serves, not just the runServeSandbox helper.
func TestServeSandboxCommandExecute(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi from laptop"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := make(chan tunnelOutcome, 1)
	hs := standInToposd(t, got)

	t.Setenv("TOPOS_TOKEN", "dev-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := newToposServeSandboxCmd()
	cmd.SetArgs([]string{"--yes", "--topos-url", hs.URL, "--root", root})
	go func() { _ = cmd.ExecuteContext(ctx) }()

	select {
	case o := <-got:
		if o.body != "hi from laptop" {
			t.Errorf("command did not serve the workspace: ReadFile = %q", o.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: the command never connected and served")
	}
}

// TestServeSandboxBearerError covers the no-bearer path: with TOPOS_TOKEN unset
// and no auth token on file (empty XDG config), the command fails before dialing
// with a sign-in hint rather than hanging or panicking.
func TestServeSandboxBearerError(t *testing.T) {
	t.Setenv("TOPOS_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no token on file → toposIdentityBearer errors

	if _, err := toposTunnelBearer(); err == nil {
		t.Fatal("toposTunnelBearer must error when no token is available")
	}
	err := runServeSandbox(context.Background(), "http://127.0.0.1:0", t.TempDir(),
		func(context.Context, string, sandbox.ExecOptions) error { return nil })
	if err == nil {
		t.Fatal("runServeSandbox must return the bearer error before dialing")
	}
}
