// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	cellaclient "latere.ai/x/cella/client"
	v1 "latere.ai/x/cella/manifest/v1"
)

// newCePortForwardCmd is `latere environments port-forward`: a listener on
// the person's loopback whose every accepted connection is carried to a port
// inside the workload over a socket of its own. A workload's port is not
// opened in the console, so this is the way to reach one.
func newCePortForwardCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "port-forward <name|id> <local>:<port>",
		Short: "Carry a local port to a port inside a workload.",
		Long: `Listen on 127.0.0.1:<local> and carry every connection made to it to
<port> inside the workload, until interrupted. A local port of 0 lets the
system pick one; the command prints the address it listens on.

The workload must be running, and the port declared in its manifest's
spec.network.ports. A declared port nothing listens on yet is not a
refusal: each connection to it ends with the reason, and a later one
reaches the port once a process holds it.`,
		Example: `  latere environments port-forward dev 8080:8080
  latere environments port-forward dev 0:5432`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			local, remote, err := forwardPorts(args[1])
			if err != nil {
				return err
			}
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return portForward(ctx, c, args[0], local, remote, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

// forwardPorts reads `<local>:<port>`: the loopback port to listen on, zero
// for one the system picks, and the port inside the workload.
func forwardPorts(arg string) (local, remote int, err error) {
	l, r, ok := strings.Cut(arg, ":")
	if !ok {
		return 0, 0, fmt.Errorf("port-forward takes <local>:<port>, not %q", arg)
	}
	local, err = strconv.Atoi(l)
	if err != nil || local < 0 || local > 65535 {
		return 0, 0, fmt.Errorf("the local port %q is not between 0 and 65535", l)
	}
	remote, err = strconv.Atoi(r)
	if err != nil || remote < 1 || remote > 65535 {
		return 0, 0, fmt.Errorf("the port %q is not between 1 and 65535", r)
	}
	return local, remote, nil
}

// portForward listens on 127.0.0.1:local and carries each connection to
// remote inside ref until ctx ends.
func portForward(ctx context.Context, c *cellaclient.Client, ref string, local, remote int, stdout, stderr io.Writer) error {
	// Only a port the manifest declares is reached, and the dial socket
	// reports an undeclared one only after it opens, so the declaration is
	// read first.
	obj, _, err := c.GetSandbox(ctx, ref)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(obj.Spec.Network.Ports, func(p v1.Port) bool { return p.Port == remote }) {
		return fmt.Errorf("port %d is not declared in %s's spec.network.ports, and only a declared port is reached; add it to the manifest and apply it again", remote, ref)
	}
	// One socket opened first answers whether the workload's port can be
	// reached at all, so a workload that is stopped is the command's exit
	// rather than a listener that fails every connection.
	probe, err := c.Dial(ctx, ref, remote)
	if err != nil {
		return err
	}
	if err := probe.Close(); err != nil {
		return err
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(local)))
	if err != nil {
		return err
	}
	closed := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer closed()
	defer func() { _ = listener.Close() }()
	if _, err := fmt.Fprintf(stdout, "Forwarding %s to port %d of %s\n", listener.Addr(), remote, ref); err != nil {
		return err
	}
	f := &forwarder{client: c, ref: ref, port: remote, stderr: stderr}
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Go(func() { f.carry(ctx, conn) })
	}
}

// forwarder carries the connections of one port-forward.
type forwarder struct {
	client *cellaclient.Client
	ref    string
	port   int
	stderr io.Writer
	// mu serializes the lines connections write to stderr.
	mu sync.Mutex
}

// carry pumps one accepted connection both ways through its own socket until
// either end closes. The socket has no half close, so the end of one
// direction ends the connection.
func (f *forwarder) carry(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stream, err := f.client.Dial(ctx, f.ref, f.port)
	if err != nil {
		f.report(err)
		return
	}
	defer func() { _ = stream.Close() }()
	ended := make(chan error, 2)
	go func() {
		_, err := io.Copy(stream, conn)
		ended <- err
	}()
	go func() {
		_, err := io.Copy(conn, stream)
		ended <- err
	}()
	// The first direction to end decides; a connection the caller closed
	// and a socket this process closed are ordinary ends, not failures.
	if err := <-ended; err != nil && !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
		f.report(err)
	}
}

// report writes one connection's failure on a line of its own.
func (f *forwarder) report(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, _ = fmt.Fprintf(f.stderr, "latere: a connection to port %d of %s ended: %v\n", f.port, f.ref, err)
}
