// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestAuthCommandsRejectExtraArguments(t *testing.T) {
	for name, factory := range map[string]func() *cobra.Command{
		"login": newAuthLoginCmd, "logout": newAuthLogoutCmd,
		"whoami": newAuthWhoamiCmd, "print-token": newAuthPrintTokenCmd,
	} {
		t.Run(name, func(t *testing.T) {
			cmd := factory()
			if err := cmd.ValidateArgs([]string{"unexpected-arg"}); err == nil {
				t.Error("unexpected positional argument accepted")
			}
			if err := cmd.ValidateArgs(nil); err != nil {
				t.Errorf("valid argument-free command rejected: %v", err)
			}
		})
	}
}
