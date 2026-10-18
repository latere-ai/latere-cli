// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// firstSignedRelease is the first release whose checksums.txt carries a
// signature. Installing an older release is refused before anything is
// downloaded: it has no signature to verify, and allowing it would let a
// downgrade step around the check.
const firstSignedRelease = "v0.21.0"

// signatureAsset is the release file holding the signature of checksums.txt.
const signatureAsset = "checksums.txt.sig"

// maxSignatureBytes caps the signature download. A signature is 88 base64
// characters, so a body anywhere near this size is not one.
const maxSignatureBytes = 1 << 10

// unsignedMessage is the one sentence a person reads when a release is
// refused for its signature, whatever was wrong with it.
const unsignedMessage = "this release is not signed by Latere; nothing was changed"

// unsignedError refuses a release whose checksums.txt carries no signature
// that verifies against a built-in key. Error is the sentence a person reads,
// and detail says what was wrong, for whoever debugs it.
type unsignedError struct {
	detail string
}

func (e *unsignedError) Error() string { return unsignedMessage }

// releaseKeysFor returns the release keys to verify tag's signature with.
// Before anything is downloaded it refuses a release older than
// firstSignedRelease, and every release when this build holds no well-formed
// release key.
func releaseKeysFor(tag string) ([]ed25519.PublicKey, error) {
	if Newer(tag, firstSignedRelease) {
		return nil, &unsignedError{detail: fmt.Sprintf("%s is older than %s, the first signed release", display(tag), firstSignedRelease)}
	}
	keys, err := PublicKeys()
	if err != nil {
		return nil, &unsignedError{detail: err.Error()}
	}
	if len(keys) == 0 {
		return nil, &unsignedError{detail: "this build holds no release key"}
	}
	return keys, nil
}

// VerifySignature reports whether sig, the contents of a release's
// checksums.txt.sig, is a valid Ed25519 signature of sums, the exact bytes of
// its checksums.txt, by one of keys. sig is the standard base64 of the 64-byte
// signature; whitespace around it is allowed and nothing else is.
func VerifySignature(sums, sig []byte, keys []ed25519.PublicKey) error {
	if len(keys) == 0 {
		return errors.New("no release key to verify against")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return fmt.Errorf("%s is not standard base64: %w", signatureAsset, err)
	}
	if len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("%s holds %d bytes; an Ed25519 signature is %d", signatureAsset, len(raw), ed25519.SignatureSize)
	}
	for i, key := range keys {
		if len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("release key %d holds %d bytes; an Ed25519 public key is %d", i, len(key), ed25519.PublicKeySize)
		}
		if ed25519.Verify(key, sums, raw) {
			return nil
		}
	}
	return fmt.Errorf("%s verifies against none of the %d release keys", signatureAsset, len(keys))
}
