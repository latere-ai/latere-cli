// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Copyright 2026 The Latere Authors. All rights reserved.
// Use of this source code is governed by an Apache-2.0
// license that can be found in the LICENSE file.

package commands

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"

	"latere.ai/x/topos/sandbox"
)

// The sandbox-tunnel wire contract. These three literals (route, subprotocol,
// descriptor) are duplicated on the Topos control plane; there is no shared
// module, so they are set deliberately on both sides and a two-process smoke
// catches any drift.
const (
	sandboxTunnelRoute       = "/v1/sandbox/tunnel"
	sandboxTunnelSubprotocol = "topos.sandbox.v1"
)

// newToposServeSandboxCmd implements `latere topos serve-sandbox`: connect this
// machine to the Topos control plane as a sandbox it can drive (mode 2). The
// edge dials the toposd WSS endpoint and serves its workspace as a
// confined+consented sandbox.Provider; a remote interactive session then runs
// its tools here, on the real files, with every command gated by a local prompt.
func newToposServeSandboxCmd() *cobra.Command {
	var (
		apiURL string
		root   string
		yes    bool
	)
	cmd := &cobra.Command{
		Use:   "serve-sandbox",
		Short: "Serve this machine as a sandbox the Topos control plane can drive.",
		Long: `Connect this machine to the Topos control plane as a sandbox it can drive.

This machine dials the control plane's sandbox-tunnel endpoint over WSS and
serves the workspace root as a confined, consented sandbox: a remote interactive
session runs its tools here, on your real files. Every command a remote session
wants to run is shown and prompted for (y/N) unless --yes is given, path access
is confined to the root, and a built-in secret deny-list (.env, .ssh, *.pem, …)
is never served.

File operations support relative symlinks whose targets stay within the root.
Absolute symlinks and symlinks leading outside the root are rejected.

For local development, set TOPOS_TOKEN to any value and point --topos-url at a
Topos server started with dev auth; the token is then accepted without a login.`,
		Example: `  latere topos serve-sandbox
  latere topos serve-sandbox --root ~/work/project --yes
  TOPOS_TOKEN=dev latere topos serve-sandbox --topos-url http://localhost:8080`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			abs, err := filepath.Abs(root)
			if err != nil {
				return fmt.Errorf("resolve --root: %w", err)
			}
			consent := promptExecConsent(os.Stdin, os.Stderr)
			if yes {
				consent = func(context.Context, string, sandbox.ExecOptions) error { return nil }
			}
			return runServeSandbox(cmd.Context(), apiURL, abs, consent)
		},
	}
	cmd.Flags().StringVar(&apiURL, "topos-url", "", "override the Topos API base URL")
	cmd.Flags().StringVar(&root, "root", ".", "workspace root to serve (default: current directory)")
	cmd.Flags().BoolVar(&yes, "yes", false, "auto-approve every remote command (skip the consent prompt)")
	return cmd
}

// runServeSandbox dials the toposd sandbox-tunnel WSS endpoint with a Topos
// bearer and serves this machine over it until the session ends or ctx is
// cancelled. It is the production transport that wraps serveSandboxTunnel: a WSS
// NetConn in place of the localhost TCP conn the tests use.
func runServeSandbox(ctx context.Context, apiURL, root string, consent sandbox.ConsentFunc) error {
	bearer, err := toposTunnelBearer(ctx)
	if err != nil {
		return err
	}
	wsURL := toWSURL(resolveToposURL(apiURL)) + sandboxTunnelRoute
	// The handshake response is never the caller's to close: websocket.Dial
	// leaves it nil on success (the connection owns the socket) and has
	// already closed it on failure.
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{ //nolint:bodyclose
		Subprotocols: []string{sandboxTunnelSubprotocol},
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + bearer}},
	})
	if err != nil {
		return fmt.Errorf("dial Topos sandbox tunnel %s: %w", wsURL, err)
	}
	defer c.CloseNow() //nolint:errcheck
	c.SetReadLimit(-1) // streams carry full request/response bodies.

	nc := websocket.NetConn(ctx, c, websocket.MessageBinary)
	fmt.Fprintf(os.Stderr, "sandbox tunnel: serving %s to %s\n", root, wsURL) //nolint:errcheck
	return serveSandboxTunnel(ctx, nc, root, consent, os.Stderr)
}

// toposTunnelBearer returns the bearer the sandbox tunnel dials with: the
// TOPOS_TOKEN dev override first (matching toposClient), else the auth-issued
// identity bearer. So TOPOS_TOKEN=dev + a dev-auth toposd is a one-step run.
func toposTunnelBearer(ctx context.Context) (string, error) {
	if v := os.Getenv("TOPOS_TOKEN"); v != "" {
		return v, nil
	}
	return toposIdentityBearer(ctx)
}

// toWSURL rewrites an http(s) base URL to its ws(s) form for a websocket dial.
// tunnel.toWS is unexported, so the sandbox path keeps its own copy.
func toWSURL(u string) string {
	switch {
	case strings.HasPrefix(u, "https://"):
		return "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		return "ws://" + strings.TrimPrefix(u, "http://")
	default:
		return u
	}
}
