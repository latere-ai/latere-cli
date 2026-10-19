// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package devices

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"latere.ai/x/pkg/atomicfile"
)

// owner is the record of whose device this computer serves: the device and
// the person (the sub of their tokens) it was connected by, or first served
// for. An engine that takes its tokens from a client refuses another
// person's, so a client signed in as someone else never makes the engine
// forget a device that is still its owner's at the platform.
type owner struct {
	Device  string `json:"device"`
	Subject string `json:"subject"`
}

// readOwner is the person recorded for device, "" when none is.
func readOwner(path, device string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var o owner
	if err = json.Unmarshal(raw, &o); err != nil {
		return "", err
	}
	if o.Device != device {
		return "", nil
	}
	return o.Subject, nil
}

// writeOwner records subject as the owner of device, mode 0600.
func writeOwner(path, device, subject string) error {
	raw, err := json.Marshal(owner{Device: device, Subject: subject})
	if err != nil {
		return err
	}
	if err = ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	return atomicfile.WriteSync(path, raw, 0o600)
}

// removeOwner deletes the record, which names a device no longer served.
func removeOwner(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
