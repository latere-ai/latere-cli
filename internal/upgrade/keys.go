// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

// releaseKeys are the Ed25519 public keys a release's checksums.txt.sig must
// verify against, each the standard base64 of the key's 32 bytes.
//
// There are at most two: the key that signs releases now and, during a
// rotation, the key that signs from the next release. A new key joins this
// list one release before it signs, and the old key leaves one release after
// the new one starts signing, so every published CLI can verify the release
// that follows it. The private half is the release pipeline's
// RELEASE_SIGNING_KEY secret, and tools/sign refuses to sign with a key this
// list does not hold.
//
// A build whose list is empty, such as a fork that removed the key, refuses
// every upgrade, and `latere devices connect` refuses to start on it.
var releaseKeys = []string{
	"x+c+oe9oKChPb7H/nFKhICCttdpjZmzOTaBF4dzWc+A=", // signs from v0.21.0
}

// maxReleaseKeys is how many keys a rotation needs: the current and the next.
const maxReleaseKeys = 2

// PublicKeys returns the built-in release keys. An error means keys.go itself
// is malformed, which TestReleaseKeysAreWellFormed keeps from being released.
func PublicKeys() ([]ed25519.PublicKey, error) {
	if len(releaseKeys) > maxReleaseKeys {
		return nil, fmt.Errorf("keys.go holds %d release keys; at most %d are allowed", len(releaseKeys), maxReleaseKeys)
	}
	keys := make([]ed25519.PublicKey, 0, len(releaseKeys))
	for i, encoded := range releaseKeys {
		raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("release key %d is not standard base64: %w", i, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("release key %d holds %d bytes; an Ed25519 public key is %d", i, len(raw), ed25519.PublicKeySize)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys, nil
}

// HasReleaseKey reports whether this build holds a well-formed release key,
// that is, whether it can verify its own upgrades. `latere devices connect`
// refuses to start when it cannot.
func HasReleaseKey() bool {
	keys, err := PublicKeys()
	return err == nil && len(keys) > 0
}

// SetReleaseKeysForTest replaces the built-in release keys with keys and
// returns a function that restores them. It exists for the end-to-end tests,
// which serve a release signed by a throwaway key to a test binary; nothing
// in the CLI calls it, so a shipped binary trusts only the keys above.
func SetReleaseKeysForTest(keys ...ed25519.PublicKey) (restore func()) {
	previous := releaseKeys
	encoded := make([]string, len(keys))
	for i, key := range keys {
		encoded[i] = base64.StdEncoding.EncodeToString(key)
	}
	releaseKeys = encoded
	return func() { releaseKeys = previous }
}
