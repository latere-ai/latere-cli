// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/latere-ai/latere-cli/internal/devices"
)

// The person's devices at the platform (platform spec 220): listed, renamed
// and removed with the person's own token. What a device is on this computer
// (the host policy file, the key) is internal/devices'. This file builds on
// every platform, since `latere logout` removes a connected computer
// wherever it runs.

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

// removeDevice removes a device at the platform, which deletes its
// environment at Cella, revokes its keys and ends its sandboxes. A device the
// platform no longer has is already removed.
func removeDevice(ctx context.Context, platformURL, id string) error {
	err := platformCall(ctx, platformURL, "", http.MethodDelete, devicesPath+"/"+id, nil, nil)
	if pe, ok := errors.AsType[*platformError](err); ok && pe.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// forgetThisDevice forgets on this computer the device it was: the key
// goes, and so does the host policy file when dropFile is set; otherwise the
// file keeps its folders and network and names no device, so the next
// connect proposes them again.
func forgetThisDevice(paths devices.Paths, id string, dropFile bool) error {
	var errs []error
	if err := paths.Keys().Delete(id); err != nil {
		errs = append(errs, fmt.Errorf("delete the key: %w", err))
	}
	path := paths.PolicyFile()
	policy, ok, err := devices.LoadPolicy(path)
	switch {
	case err != nil:
		errs = append(errs, err)
	case !ok || policy.Device != id:
	case dropFile:
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("delete the host policy file: %w", err))
		}
	default:
		policy.Device = ""
		if err := devices.SavePolicy(path, policy); err != nil {
			errs = append(errs, fmt.Errorf("update the host policy file: %w", err))
		}
	}
	return errors.Join(errs...)
}

// leaveBeforeLogout is what `latere logout` does on a connected computer:
// it says that signing out removes the computer from the person's devices,
// asks, and removes it at the platform and here before the login is
// revoked. A computer that is not connected is signed out of as before.
func leaveBeforeLogout(ctx context.Context, in io.Reader, errOut io.Writer, platformURL string, yes bool) error {
	paths, err := devices.DefaultPaths()
	if err != nil {
		return nil //nolint:nilerr // with no home there is no host policy file, so no connected computer to leave
	}
	policy, ok, err := devices.LoadPolicy(paths.PolicyFile())
	if err != nil {
		return devices.Fail(devices.CodePolicyInvalid, "", err)
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
			return devices.Fail(devices.CodeLogoutCancelled, fmt.Sprintf("the answer was %q", line), err)
		}
	}
	if err := removeDevice(ctx, platformURL, policy.Device); err != nil {
		return devices.Fail(devices.CodeLogoutBlocked, "", wrapReposErr(err))
	}
	if err := forgetThisDevice(paths, policy.Device, false); err != nil {
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
