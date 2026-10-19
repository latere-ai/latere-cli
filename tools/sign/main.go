// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Command sign signs a release's checksums.txt and makes the keys it signs
// with.
//
//	go run ./tools/sign <file> <signature-file>
//	go run ./tools/sign -sshsig <file> <signature-file>
//	go run ./tools/sign -keygen <private-key-file>
//
// GoReleaser runs the first two forms over checksums.txt (.goreleaser.yaml,
// `signs`), with the same key. The private key is the RELEASE_SIGNING_KEY
// environment variable, the standard base64 of a 32-byte Ed25519 seed.
//
// The first form writes checksums.txt.sig, the standard base64 of the 64-byte
// signature, which `latere upgrade` verifies against internal/upgrade/keys.go
// before it reads any checksum. The second writes checksums.txt.sshsig, the
// same key's signature in OpenSSH's signature format (PROTOCOL.sshsig) with
// the namespace latere-release, which the installer verifies with
// `ssh-keygen -Y verify`: stock macOS has no other Ed25519 verifier.
//
// Signing fails, and the release with it, when the variable is unset or
// malformed or its public key is not one keys.go holds, since every CLI would
// refuse what it signed.
//
// The third form makes a key for a rotation: it writes a new private key, in
// the encoding RELEASE_SIGNING_KEY takes, to a file only its owner can read,
// and prints the public key to add to keys.go.
package main

import (
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
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

const usage = "usage: sign [-sshsig] <file> <signature-file> | sign -keygen <private-key-file>"

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
		return sign(args[0], args[1], getenv(keyEnv), keys, rawSignature)
	case len(args) == 3 && args[0] == "-sshsig" && !strings.HasPrefix(args[1], "-"):
		return sign(args[1], args[2], getenv(keyEnv), keys, sshSignature)
	default:
		return errors.New(usage)
	}
}

// sign writes the signature of the file at path to out, in the format
// format writes, with the private key encoded as RELEASE_SIGNING_KEY takes
// it.
func sign(path, out, encoded string, keys []ed25519.PublicKey, format func(ed25519.PrivateKey, []byte) []byte) error {
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
	if err := os.WriteFile(out, format(private, data), 0o644); err != nil { //nolint:gosec // a signature is public
		return fmt.Errorf("write %s: %w", out, err)
	}
	return nil
}

// rawSignature is checksums.txt.sig: the standard base64 of the 64-byte
// Ed25519 signature of data, with no newline.
func rawSignature(private ed25519.PrivateKey, data []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, data)))
}

// The fixed fields of an SSH signature (OpenSSH's PROTOCOL.sshsig).
const (
	sshsigMagic   = "SSHSIG"
	sshsigVersion = 1
	sshsigHash    = "sha512"
	// sshsigColumns is the width OpenSSH wraps the armored base64 at.
	sshsigColumns = 70
)

// sshSignature is checksums.txt.sshsig: the Ed25519 signature of data in
// OpenSSH's signature format, the namespace upgrade.SSHNamespace, armored as
// `ssh-keygen -Y sign` writes it. What is signed is not data itself but the
// magic, the namespace, an empty reserved field, the hash's name and data's
// SHA-512, so the signature verifies for that namespace alone.
func sshSignature(private ed25519.PrivateKey, data []byte) []byte {
	digest := sha512.Sum512(data)
	signed := []byte(sshsigMagic)
	signed = sshString(signed, []byte(upgrade.SSHNamespace))
	signed = sshString(signed, nil)
	signed = sshString(signed, []byte(sshsigHash))
	signed = sshString(signed, digest[:])
	// Public always returns an ed25519.PublicKey, as in sign.
	public, _ := private.Public().(ed25519.PublicKey)
	sig := sshString(nil, []byte("ssh-ed25519"))
	sig = sshString(sig, ed25519.Sign(private, signed))

	blob := []byte(sshsigMagic)
	blob = binary.BigEndian.AppendUint32(blob, sshsigVersion)
	blob = sshString(blob, upgrade.SSHPublicKey(public))
	blob = sshString(blob, []byte(upgrade.SSHNamespace))
	blob = sshString(blob, nil)
	blob = sshString(blob, []byte(sshsigHash))
	blob = sshString(blob, sig)

	encoded := base64.StdEncoding.EncodeToString(blob)
	var b strings.Builder
	b.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(encoded) > sshsigColumns {
		b.WriteString(encoded[:sshsigColumns] + "\n")
		encoded = encoded[sshsigColumns:]
	}
	b.WriteString(encoded + "\n-----END SSH SIGNATURE-----\n")
	return []byte(b.String())
}

// sshString appends b to buf as an SSH wire string (RFC 4251): its length as
// a big-endian uint32, then its bytes.
func sshString(buf, b []byte) []byte {
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(b))) //nolint:gosec // every field written is far below 4 GiB
	return append(buf, b...)
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
