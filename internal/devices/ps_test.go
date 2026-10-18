// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package devices

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// psState is a process's state letter: Z for a zombie, which has ended and
// waits to be reaped.
func psState(pid int) (string, error) {
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return "", err
		}
		// The state follows the command's closing parenthesis.
		_, rest, ok := strings.Cut(string(raw), ") ")
		if !ok {
			return "", fmt.Errorf("unreadable /proc/%d/stat", pid)
		}
		return rest[:1], nil
	}
	out, err := exec.Command("/bin/ps", "-o", "stat=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
