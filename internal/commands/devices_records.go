// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	cellaclient "latere.ai/x/cella/client"
	"latere.ai/x/cella/device"
	"latere.ai/x/sdk-go/account"
	"latere.ai/x/sdk-go/latere"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The person's devices at the platform (platform spec 220): listed, renamed
// and removed with the person's own token. What a device is on this computer
// (the host policy file, the key) is Cella's device engine's. This file
// builds on every platform, since `latere logout` removes a connected
// computer wherever it runs.

// devicesPath is the platform's list of the person's devices.
const devicesPath = "/v1/account/devices"

// deviceRecord is one device as the platform lists it.
type deviceRecord struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Status   string     `json:"status"`
	Online   bool       `json:"online"`
	OS       string     `json:"os"`
	Arch     string     `json:"arch"`
	LastSeen *time.Time `json:"last_seen"`
	Folders  []struct {
		Path  string `json:"path"`
		Write bool   `json:"write"`
	} `json:"folders"`
	Network *struct {
		Mode  string   `json:"mode"`
		Hosts []string `json:"hosts"`
	} `json:"network"`
	CreatedAt time.Time `json:"created_at"`
}

// deviceList is the platform's answer to the list: the devices, and how many
// one person may connect.
type deviceList struct {
	Items []deviceRecord `json:"items"`
	Limit int            `json:"limit"`
}

// platformRequest is one request to the platform with a bearer its caller
// chose, decoding a JSON answer into out.
type platformRequest func(ctx context.Context, method, path string, body, out any) error

// deviceAuth is where a device's calls as the person get their bearers: the
// saved login, or under `serve --credentials client` the tokens the client
// handed the engine, in which mode the saved login is never read.
type deviceAuth struct {
	apiURL, platformURL string
	// held is the client's tokens; nil is the saved login.
	held *device.Credentials
}

// cella is the Cella client as the person.
func (a deviceAuth) cella() (*cellaclient.Client, error) {
	if a.held == nil {
		return cellaClient(a.apiURL)
	}
	base, err := resolveCellaURL(a.apiURL)
	if err != nil {
		return nil, err
	}
	held := a.held
	return cellaclient.New(cellaclient.Config{
		URL: base, HTTPClient: cellaHTTPClient(), UserAgent: "latere-cli",
		Token: cellaclient.TokenFunc(func(ctx context.Context) (string, error) { return held.Token(ctx, device.AudienceCella) }),
	})
}

// platform sends one request to the platform as the person.
func (a deviceAuth) platform(ctx context.Context, method, path string, body, out any) error {
	return platformCallWith(ctx, a.platformURL, a.bearer, method, path, body, out)
}

// bearer is the person's bearer for the platform at base: the client's
// platform token, or one minted from the saved login.
func (a deviceAuth) bearer(ctx context.Context, base string) (string, error) {
	if a.held != nil {
		return a.held.Token(ctx, latere.DefaultAudience)
	}
	return platformBearer(ctx, base, "")
}

// account is the SDK's account client at the platform, as the person.
func (a deviceAuth) account(ctx context.Context) (*account.Client, error) {
	base := resolvePlatformURL(a.platformURL)
	bearer, err := a.bearer(ctx, base)
	if err != nil {
		return nil, err
	}
	cfg, err := api.Product{BaseURL: base, Token: bearer}.Config()
	if err != nil {
		return nil, err
	}
	return account.New(cfg), nil
}

// orgContext is the organization the person's bearer is in, "" for their
// personal context, read from the saved login or the client's platform
// token.
func (a deviceAuth) orgContext(ctx context.Context) (string, error) {
	var raw string
	if a.held != nil {
		token, err := a.held.Token(ctx, latere.DefaultAudience)
		if err != nil {
			return "", device.Fail(device.CodeCredentialsNeeded, "", err)
		}
		raw = token
	} else {
		tok, err := api.LoadAuthToken()
		if err != nil {
			return "", device.Fail(device.CodeNoSignIn, "", err)
		}
		raw = tok.AccessToken
	}
	info, err := principalFromJWT(raw)
	if err != nil {
		return "", device.Fail(device.CodeNoSignIn, "", err)
	}
	return info.OrgID, nil
}

// remove removes a device at the platform, which deletes its environment at
// Cella, revokes its keys and ends its sandboxes. A device the platform no
// longer has is already removed.
func (a deviceAuth) remove(ctx context.Context, id string) error {
	err := a.platform(ctx, http.MethodDelete, devicesPath+"/"+id, nil, nil)
	if pe, ok := errors.AsType[*platformError](err); ok && pe.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// laterePlatform is the platform's part of a connect, for the engine's
// Enrollment: the tenant labels a create carries, the sign-in's context, the
// device's name and its removal in the platform's records, and how many
// devices the person has.
type laterePlatform struct{ auth deviceAuth }

// Labels are the tenant labels of the sign-in's context.
func (p laterePlatform) Labels(ctx context.Context) (map[string]string, error) {
	return readTenantLabels(ctx, p.auth.platform)
}

// Context is the organization the sign-in is in, "" for the person's own.
func (p laterePlatform) Context(ctx context.Context) (string, error) {
	return p.auth.orgContext(ctx)
}

// Rename names the device in the platform's records.
func (p laterePlatform) Rename(ctx context.Context, id, name string) error {
	return p.auth.platform(ctx, http.MethodPatch, devicesPath+"/"+id, map[string]string{"name": name}, nil)
}

// Remove removes the device at the platform; one it no longer has is
// already removed.
func (p laterePlatform) Remove(ctx context.Context, id string) error {
	return p.auth.remove(ctx, id)
}

// Devices answers how many devices the person has and how many one person
// may connect.
func (p laterePlatform) Devices(ctx context.Context) (have, limit int, err error) {
	client, err := p.auth.account(ctx)
	if err != nil {
		return 0, 0, err
	}
	list, err := client.ListDevices(ctx)
	if err != nil {
		return 0, 0, err
	}
	return len(list.Items), list.Limit, nil
}

// leaveBeforeLogout is what `latere logout` does on a connected computer:
// it says that signing out removes the computer from the person's devices,
// asks, and removes it at the platform and here before the login is
// revoked. A computer that is not connected is signed out of as before.
func leaveBeforeLogout(ctx context.Context, in io.Reader, errOut io.Writer, platformURL string, yes bool) error {
	paths, err := device.DefaultPaths()
	if err != nil {
		return nil //nolint:nilerr // with no home there is no host policy file, so no connected computer to leave
	}
	policy, ok, err := device.LoadPolicy(paths.PolicyFile())
	if err != nil {
		return device.Fail(device.CodePolicyInvalid, "", err)
	}
	if !ok || policy.Device == "" {
		return nil
	}
	fprintf(errOut, "This computer is connected as your device %q (%s). Signing out removes it from your devices,\nand sessions can no longer use it.\n", policy.Name, policy.Device)
	if !yes {
		fprint(errOut, "Sign out and remove this computer? [y/N] ")
		line, err := readLine(in)
		if err != nil || !strings.EqualFold(line, "y") && !strings.EqualFold(line, "yes") {
			fprintln(errOut)
			return cliRefusal(codeLogoutCancelled, fmt.Sprintf("the answer was %q", line), err)
		}
	}
	if err := (deviceAuth{platformURL: platformURL}).remove(ctx, policy.Device); err != nil {
		return cliRefusal(codeLogoutBlocked, "", wrapReposErr(err))
	}
	if err := device.Forget(paths.Keys(), paths.PolicyFile(), policy.Device, false); err != nil {
		fprintf(errOut, "  warning: this computer was removed from your devices, and %v\n", err)
	}
	fprintln(errOut, "Removed this computer from your devices.")
	return nil
}

// readLine reads one line, trimmed.
func readLine(in io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
	}
	return strings.TrimSpace(b.String()), nil
}
