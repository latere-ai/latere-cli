// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package devices

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestMain(m *testing.M) {
	// Every key a test keeps lands in an in-memory keychain, never the
	// developer's own.
	keyring.MockInit()
	os.Exit(m.Run())
}
