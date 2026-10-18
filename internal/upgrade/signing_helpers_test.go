// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

// newTestKey returns a throwaway Ed25519 key pair. No test signs with the
// release key itself: its private half exists only in the release pipeline.
func newTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

// useTestKey makes a throwaway key the only release key for the rest of the
// test and returns its private half.
func useTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	public, private := newTestKey(t)
	t.Cleanup(SetReleaseKeysForTest(public))
	return private
}

// signSums returns the checksums.txt.sig body for sums, as tools/sign writes it.
func signSums(private ed25519.PrivateKey, sums string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(private, []byte(sums)))
}
