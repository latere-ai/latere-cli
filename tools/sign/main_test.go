// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/upgrade"
)

// fixtureSums is a checksums.txt as GoReleaser writes it.
const fixtureSums = "1f2e3d4c5b6a79881f2e3d4c5b6a79881f2e3d4c5b6a79881f2e3d4c5b6a7988  latere_0.21.0_linux_amd64.tar.gz\n" +
	"8897a6b5c4d3e2f18897a6b5c4d3e2f18897a6b5c4d3e2f18897a6b5c4d3e2f1  latere_0.21.0_darwin_arm64.tar.gz\n"

// throwawayKey returns a test key pair and its private key encoded as
// RELEASE_SIGNING_KEY takes it. The release key itself never signs in a test.
func throwawayKey(t *testing.T) (ed25519.PublicKey, string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return public, base64.StdEncoding.EncodeToString(private.Seed())
}

func env(value string) func(string) string {
	return func(name string) string {
		if name == keyEnv {
			return value
		}
		return ""
	}
}

func writeFixture(t *testing.T) (dir, sums string) {
	t.Helper()
	dir = t.TempDir()
	sums = filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(sums, []byte(fixtureSums), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, sums
}

// The signature tools/sign writes is the one `latere upgrade` verifies.
func TestSignsAFixtureTheCLIVerifies(t *testing.T) {
	public, encoded := throwawayKey(t)
	dir, sums := writeFixture(t)
	out := filepath.Join(dir, "checksums.txt.sig")
	// Surrounding whitespace, as a secret pasted with a newline has, is fine.
	if err := run([]string{sums, out}, env(encoded+"\n"), []ed25519.PublicKey{public}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 88 {
		t.Errorf("signature is %d bytes, want the 88 base64 characters of 64 bytes: %q", len(sig), sig)
	}
	if err := upgrade.VerifySignature([]byte(fixtureSums), sig, []ed25519.PublicKey{public}); err != nil {
		t.Errorf("the CLI refuses the signature: %v", err)
	}
	if err := upgrade.VerifySignature([]byte(fixtureSums+"x"), sig, []ed25519.PublicKey{public}); err == nil {
		t.Error("the signature verifies other content")
	}
}

func TestSignRefusesWithoutAUsableKey(t *testing.T) {
	public, encoded := throwawayKey(t)
	otherPublic, _ := throwawayKey(t)
	short := base64.StdEncoding.EncodeToString(make([]byte, 31))
	for _, tc := range []struct {
		name, key string
		keys      []ed25519.PublicKey
		want      string
	}{
		{"unset", "", []ed25519.PublicKey{public}, "RELEASE_SIGNING_KEY is not set"},
		{"blank", " \n", []ed25519.PublicKey{public}, "RELEASE_SIGNING_KEY is not set"},
		{"not base64", "not base64!", []ed25519.PublicKey{public}, "not standard base64"},
		{"wrong length", short, []ed25519.PublicKey{public}, "holds 31 bytes; an Ed25519 seed is 32"},
		{"not a built-in key", encoded, []ed25519.PublicKey{otherPublic}, "is not in internal/upgrade/keys.go"},
		{"no built-in key", encoded, nil, "is not in internal/upgrade/keys.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, sums := writeFixture(t)
			out := filepath.Join(dir, "checksums.txt.sig")
			err := run([]string{sums, out}, env(tc.key), tc.keys, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), encoded) {
				t.Error("the error names the private key")
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a signature was written: %v", err)
			}
		})
	}
}

// The release key in keys.go is what a release is checked against, so a key
// it does not hold cannot sign one.
func TestSignChecksTheKeyAgainstKeysGo(t *testing.T) {
	keys, err := upgrade.PublicKeys()
	if err != nil {
		t.Fatal(err)
	}
	_, encoded := throwawayKey(t)
	dir, sums := writeFixture(t)
	err = run([]string{sums, filepath.Join(dir, "checksums.txt.sig")}, env(encoded), keys, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "is not in internal/upgrade/keys.go") {
		t.Fatalf("error = %v, want the key refused", err)
	}
}

func TestSignReportsFileErrors(t *testing.T) {
	public, encoded := throwawayKey(t)
	keys := []ed25519.PublicKey{public}
	dir, sums := writeFixture(t)
	if err := run([]string{filepath.Join(dir, "absent"), filepath.Join(dir, "out.sig")}, env(encoded), keys, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "read ") {
		t.Errorf("missing input: error = %v", err)
	}
	if err := run([]string{sums, filepath.Join(dir, "absent", "out.sig")}, env(encoded), keys, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "write ") {
		t.Errorf("unwritable output: error = %v", err)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"checksums.txt"}, {"-x", "out"}, {"a", "b", "c"}, {"-keygen"}} {
		if err := run(args, env(""), nil, &bytes.Buffer{}); err == nil || err.Error() != usage {
			t.Errorf("run(%q) error = %v, want the usage", args, err)
		}
	}
}

func TestKeygenMakesAKeyThatSigns(t *testing.T) {
	dir, sums := writeFixture(t)
	keyFile := filepath.Join(dir, "release-signing-key")
	var stdout bytes.Buffer
	if err := run([]string{"-keygen", keyFile}, env(""), nil, &stdout); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}
	encoded, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	public, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout.String()))
	if err != nil || len(public) != ed25519.PublicKeySize {
		t.Fatalf("printed public key %q: %v", stdout.String(), err)
	}
	if strings.Contains(stdout.String(), string(encoded)) {
		t.Error("keygen printed the private key")
	}
	out := filepath.Join(dir, "checksums.txt.sig")
	if err := run([]string{sums, out}, env(string(encoded)), []ed25519.PublicKey{public}, &bytes.Buffer{}); err != nil {
		t.Fatalf("the new key does not sign for its printed public key: %v", err)
	}

	// An existing key is never overwritten.
	if err := run([]string{"-keygen", keyFile}, env(""), nil, &bytes.Buffer{}); err == nil {
		t.Error("keygen overwrote an existing key")
	}
	if again, err := os.ReadFile(keyFile); err != nil || !bytes.Equal(again, encoded) {
		t.Errorf("the existing key changed: %v", err)
	}
}

// main's own path: the built-in keys, the exit status and the message.
func TestCLI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := cli([]string{"absent", "absent.sig"}, env(""), &stdout, &stderr); status != 1 ||
		stderr.String() != "sign: RELEASE_SIGNING_KEY is not set; a release is never published unsigned\n" {
		t.Errorf("status = %d, stderr = %q", status, stderr.String())
	}
	stderr.Reset()
	keyFile := filepath.Join(t.TempDir(), "key")
	if status := cli([]string{"-keygen", keyFile}, env(""), &stdout, &stderr); status != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
		t.Errorf("keygen: status = %d, stdout = %q, stderr = %q", status, stdout.String(), stderr.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestKeygenReportsErrors(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"-keygen", filepath.Join(dir, "absent", "key")}, env(""), nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "create ") {
		t.Errorf("uncreatable key file: error = %v", err)
	}
	keyFile := filepath.Join(dir, "key")
	err := run([]string{"-keygen", keyFile}, env(""), nil, failingWriter{})
	if err == nil || !strings.Contains(err.Error(), "the private key is in "+keyFile) {
		t.Errorf("unprintable public key: error = %v", err)
	}
}
