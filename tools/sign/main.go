// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Command sign signs a release's checksums.txt and makes the keys it signs
// with.
//
//	go run ./tools/sign <file> <signature-file>
//	go run ./tools/sign -keygen <private-key-file>
//
// GoReleaser runs the first form over checksums.txt (.goreleaser.yaml,
// `signs`). The private key is the RELEASE_SIGNING_KEY environment variable,
// the standard base64 of a 32-byte Ed25519 seed, and the signature file holds
// the standard base64 of the 64-byte signature, which `latere upgrade`
// verifies against internal/upgrade/keys.go before it reads any checksum.
// Signing fails, and the release with it, when the variable is unset or
// malformed or its public key is not one keys.go holds, since every CLI would
// refuse what it signed.
//
// The second form makes a key for a rotation: it writes a new private key, in
// the encoding RELEASE_SIGNING_KEY takes, to a file only its owner can read,
// and prints the public key to add to keys.go.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/latere-ai/latere-cli/internal/upgrade"
)

// keyEnv names the variable holding the private signing key.
const keyEnv = "RELEASE_SIGNING_KEY"

const usage = "usage: sign <file> <signature-file> | sign -keygen <private-key-file>"

func main() { os.Exit(cli(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }

// cli runs one invocation against the release keys built into the CLI and
// returns the process's exit status.
func cli(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	keys, err := upgrade.PublicKeys()
	if err == nil {
		err = run(args, getenv, keys, stdout)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "sign:", err)
		return 1
	}
	return 0
}

// run carries out one invocation. keys are the release keys the CLI trusts;
// a signature that does not verify against them is never written.
func run(args []string, getenv func(string) string, keys []ed25519.PublicKey, stdout io.Writer) error {
	switch {
	case len(args) == 2 && args[0] == "-keygen":
		return keygen(args[1], stdout)
	case len(args) == 2 && !strings.HasPrefix(args[0], "-"):
		return sign(args[0], args[1], getenv(keyEnv), keys)
	default:
		return errors.New(usage)
	}
}

// sign writes the signature of the file at path to out, with the private key
// encoded as RELEASE_SIGNING_KEY takes it.
func sign(path, out, encoded string, keys []ed25519.PublicKey) error {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return fmt.Errorf("%s is not set; a release is never published unsigned", keyEnv)
	}
	seed, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("%s is not standard base64: %w", keyEnv, err)
	}
	if len(seed) != ed25519.SeedSize {
		return fmt.Errorf("%s holds %d bytes; an Ed25519 seed is %d", keyEnv, len(seed), ed25519.SeedSize)
	}
	private := ed25519.NewKeyFromSeed(seed)
	// Public always returns an ed25519.PublicKey; were it ever not one, the
	// nil key would match no release key and signing would stop below.
	public, _ := private.Public().(ed25519.PublicKey)
	if !slices.ContainsFunc(keys, func(k ed25519.PublicKey) bool { return k.Equal(public) }) {
		return fmt.Errorf("the public key of %s, %s, is not in internal/upgrade/keys.go, so every CLI would refuse what it signs",
			keyEnv, base64.StdEncoding.EncodeToString(public))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(private, data))
	if err := os.WriteFile(out, []byte(signature), 0o644); err != nil { //nolint:gosec // a signature is public
		return fmt.Errorf("write %s: %w", out, err)
	}
	return nil
}

// keygen writes a new private key to path, readable by its owner alone and
// never over an existing file, and prints its public key to stdout.
func keygen(path string, stdout io.Writer) error {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return fmt.Errorf("generate a key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	_, writeErr := file.WriteString(base64.StdEncoding.EncodeToString(private.Seed()))
	if err := errors.Join(writeErr, file.Close()); err != nil {
		// A partial key is removed so it is never mistaken for a whole one.
		return errors.Join(fmt.Errorf("write %s: %w", path, err), os.Remove(path))
	}
	if _, err := fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(public)); err != nil {
		return fmt.Errorf("print the public key (the private key is in %s): %w", path, err)
	}
	return nil
}
