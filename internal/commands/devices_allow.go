// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"latere.ai/x/cella/device"
	"latere.ai/x/cella/runtime/local"
	"latere.ai/x/pkg/httpjson"
)

// `latere devices allow`: a host a session's command was refused, allowed
// for that session through the engine serving this computer, or saved to
// the host policy file for every session.

func newDevicesAllowCmd() *cobra.Command {
	var (
		session string
		save    bool
	)
	cmd := &cobra.Command{
		Use:   "allow HOST[:PORT] (--session ID | --save)",
		Args:  cobra.ExactArgs(1),
		Short: "Let one session, or every session, reach a host this computer refused.",
		Long: `Let sessions reach a host their commands were refused. 'latere devices
connect' prints both commands under each refused host.

--session ID allows it for that session alone, the session or the sandbox
the refused line names: the session's next command reaches it, until its
sandbox stops on this computer or connect stops. The host policy file is
not changed. It needs 'latere devices connect' or 'serve' running.

--save writes it into the host policy file, for every session: under the
network allowlist the host joins network.hosts; under any, a block of
that host by name is lifted. While the computer serves, every session's
next command reaches it at once and no session stops; otherwise it applies
from the next connect.

A command already running keeps the network it started with. A host
alone, or with port 443 or 80, is allowed on both 443 and 80; another
port is allowed alone. Nothing is allowed under the network none, nor a
host a *. entry blocks: narrow or remove that entry in the file.`,
		Example: `  latere devices allow api.example.com --session ses_01jaq7v3k9m2wxyz
  latere devices allow api.example.com:443 --save
  latere devices allow db.example.com:5432 --save`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !save && strings.TrimSpace(session) == "" {
				return cliRefusal(codeNoSession, "--session names no session", nil)
			}
			host, port, err := splitAllowed(args[0])
			if err != nil {
				return err
			}
			entry, err := local.HostEntry(host, port)
			if err != nil {
				return device.Fail(device.CodeHostNotAllowed, err.Error(), err)
			}
			paths, err := device.DefaultPaths()
			if err != nil {
				return device.Fail(device.CodeServeFailed, "resolve the state directory", err)
			}
			errOut := cmd.ErrOrStderr()
			body := map[string]any{"host": host, "port": port}
			if !save {
				if err = controlSend(cmd.Context(), paths.Socket(), "/v1/sessions/"+url.PathEscape(session)+"/hosts", body); err != nil {
					return err
				}
				fprintf(errOut, "Allowed %s for %s: its next command reaches it, until its sandbox stops on this computer. The host policy file is unchanged.\n", entry, session)
				return nil
			}
			err = controlSend(cmd.Context(), paths.Socket(), "/v1/policy/hosts", body)
			switch {
			case err == nil:
				fprintf(errOut, "Saved %s to the host policy file: every session's next command reaches it.\n", entry)
			case device.CodeOf(err) == device.CodeNotServing:
				// Nothing serves: the file is changed as the engine changes
				// it, checked the same way, and read at the next connect.
				if _, _, err = loadPolicy(paths.PolicyFile()); err != nil {
					return err
				}
				if err = device.New(device.Config{Paths: paths, NewDriver: sessionDriver}).SaveHost(host, port); err != nil {
					return err
				}
				fprintf(errOut, "Saved %s to the host policy file: sessions reach it from the next `latere devices connect`.\n", entry)
			default:
				return err
			}
			if p, _, err := device.LoadPolicy(paths.PolicyFile()); err == nil && p.Network.Mode == device.NetworkAny {
				fprintln(errOut, olderLatereNote)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&session, "session", "", "allow it for this session alone, by the id the refused line names, until its sandbox stops here")
	f.BoolVar(&save, "save", false, "save it to the host policy file, for every session")
	cmd.MarkFlagsMutuallyExclusive("session", "save")
	cmd.MarkFlagsOneRequired("session", "save")
	return cmd
}

// splitAllowed is the host and the port of HOST[:PORT], 443 when it names
// none. An IPv6 address with a port is written in brackets.
func splitAllowed(arg string) (string, int, error) {
	host, portText, err := net.SplitHostPort(arg)
	if err != nil {
		// No port: 443, which allows the host on 443 and 80 alike.
		host, portText = strings.TrimSuffix(strings.TrimPrefix(arg, "["), "]"), "443"
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, device.Fail(device.CodeHostNotAllowed, fmt.Sprintf("%q names no port number after the colon", arg), err)
	}
	return host, port, nil
}

// controlSend posts body as JSON to route on the control socket of the
// engine serving this computer, for a route that answers 204 No Content.
// Nothing listening is device_not_serving; a refusal is the engine's own
// code, with its sentence and its detail.
func controlSend(ctx context.Context, socket, route string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return device.Fail(device.CodeControlUnavailable, "", err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://device"+route, bytes.NewReader(raw))
	if err != nil {
		return device.Fail(device.CodeControlUnavailable, "", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		// No socket, or one an engine that ended left: nothing serves.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return device.Fail(device.CodeNotServing, "no engine answers at "+socket, err)
		}
		return device.Fail(device.CodeControlUnavailable, "", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNoContent || res.StatusCode == http.StatusOK {
		return nil
	}
	var refusal httpjson.ErrorEnvelope
	if err := json.NewDecoder(res.Body).Decode(&refusal); err != nil {
		return device.Fail(device.CodeControlUnavailable, "POST "+route+" answered "+res.Status, err)
	}
	detail, _ := refusal.Error.Details["detail"].(string)
	switch code := refusal.Error.Code; {
	case code == "not_serving":
		return device.Fail(device.CodeNotServing, detail, nil)
	case code == "no_session":
		return cliRefusal(codeNoSession, detail, nil)
	case device.Fail(code, "", nil).Message != "":
		return device.Fail(code, detail, nil)
	}
	return device.Fail(device.CodeControlUnavailable,
		fmt.Sprintf("POST %s answered %s: %s: %s", route, res.Status, refusal.Error.Code, refusal.Error.Message), nil)
}
