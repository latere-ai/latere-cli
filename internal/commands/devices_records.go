// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/latere-ai/latere-cli/internal/devices"
)

// The person's devices at the platform (platform spec 220): listed, renamed
// and removed with the person's own token. What a device is on this computer
// (the host policy file, the key) is internal/devices'.

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
	if err := devices.OpenKeys(paths.KeyFile()).Delete(id); err != nil {
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
