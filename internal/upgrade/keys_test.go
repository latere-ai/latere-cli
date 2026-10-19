// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"bytes"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

// releaseAllowedSigners is the allowed signers line the installer embeds to
// verify checksums.txt.sshsig, written out so a change to the release key
// changes it here too, and the installer with it.
const releaseAllowedSigners = "latere-release ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMfnPqHvaCgoT2+x/5xSoSAgrbXaY2Zszk2gReHc1nPg"

func TestAllowedSignersLine(t *testing.T) {
	lines, err := AllowedSigners()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(lines, []string{releaseAllowedSigners}) {
		t.Errorf("AllowedSigners() = %q, want %q", lines, releaseAllowedSigners)
	}
	keys, err := PublicKeys()
	if err != nil {
		t.Fatal(err)
	}
	// The line's key is the release key itself: its wire encoding carries
	// the type and then the 32 bytes keys.go holds.
	fields := strings.Fields(releaseAllowedSigners)
	if len(fields) != 3 {
		t.Fatalf("the allowed signers line has %d fields; want principal, type and key", len(fields))
	}
	blob, err := base64.StdEncoding.DecodeString(fields[2])
	if err != nil {
		t.Fatal(err)
	}
	if fields[0] != SSHNamespace || fields[1] != "ssh-ed25519" ||
		!bytes.Equal(blob, append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20"), keys[0]...)) {
		t.Errorf("the allowed signers line does not name the release key: %q", releaseAllowedSigners)
	}
}

func TestAllowedSignersFollowsTheKeys(t *testing.T) {
	a, _ := newTestKey(t)
	b, _ := newTestKey(t)
	t.Cleanup(SetReleaseKeysForTest(a, b))
	lines, err := AllowedSigners()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(lines, []string{AllowedSigner(a), AllowedSigner(b)}) {
		t.Errorf("AllowedSigners() = %q, want a line for each release key", lines)
	}
	previous := releaseKeys
	releaseKeys = []string{"not base64!"}
	t.Cleanup(func() { releaseKeys = previous })
	if _, err := AllowedSigners(); err == nil {
		t.Error("AllowedSigners answered lines for a malformed key")
	}
}
