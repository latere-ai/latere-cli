---
title: "Signed upgrades: each release signs its checksums with an Ed25519 key held by the release pipeline, and `latere upgrade`, auto-upgrade and the installer verify the signature against keys built into the binary before trusting any checksum"
status: complete
depends_on:
  - "CROSS-REPO: latere-ai/platform specs/186-the-latere-command-installs-from-the-platforms-address.md, the installer that verifies the same signature"
affects:
  - .goreleaser.yaml (`signs` over `checksums.txt`)
  - tools/sign (new: the signer GoReleaser runs, the raw and the SSH signature, and the key generator for a rotation)
  - "CROSS-REPO: latere-ai/ci .github/workflows/cli-release.yml (the optional RELEASE_SIGNING_KEY secret in the GoReleaser step's environment)"
  - internal/upgrade/release.go (verify before `checksumFor`)
  - internal/upgrade/keys.go (new: the public keys, and the allowed signers line the installer embeds)
  - internal/upgrade/signature.go (new: the verification, the refusal, the first signed release)
  - internal/upgrade/notify.go, internal/upgrade/state.go (the refusal in `latere upgrade` and auto-upgrade)
  - internal/commands/upgrade.go (the help)
  - "CROSS-REPO: latere-ai/platform (the installer script verifies `checksums.txt.sig`)"
  - docs/configuration.md, README.md, CONTRIBUTING.md, CHANGELOG.md
effort: small
created: 2026-10-18
updated: 2026-10-19
author: changkun
---

# Signed upgrades

## Why

`latere upgrade` and the daily auto-upgrade replace the binary in place.
`internal/upgrade/release.go:126` downloads `checksums.txt` from the same
release as the archive and compares the archive's SHA-256 with it. That
catches a broken download. It does not catch a release that was
replaced, because whoever can publish the archive can publish its
checksum.

Until now the cost of that gap was one person's CLI. `latere devices`
(spec 016) makes the CLI the program that runs an agent's commands on a
person's computer, so a replaced release would reach every connected
computer through its next auto-upgrade. The signature has to come first.

## Design

### Signing

The release pipeline signs `checksums.txt` with an Ed25519 private key
kept as a secret of the repository's release environment, writing
`checksums.txt.sig` beside it (GoReleaser's `signs`, with a command that
produces the raw 64-byte signature, base64-encoded). Nothing else is
signed: every archive is covered by its line in the checksums.

### Verifying

`internal/upgrade/keys.go` holds the public keys, at most two: the
current one and the next. Before reading any line of `checksums.txt`,
the upgrade downloads `checksums.txt.sig` and verifies it with
`crypto/ed25519` against either key. A missing or invalid signature
stops the upgrade with "this release is not signed by Latere; nothing
was changed", and auto-upgrade records the same and leaves the binary
alone. There is no flag to skip it. A person who wants an unsigned build
installs it by hand.

`latere upgrade vX.Y.Z` to a release older than the first signed one is
refused the same way, so the check cannot be stepped around by a
downgrade.

### Rotation

A new key ships as the "next" key in one release and signs from the
release after it. The old key leaves the binary one release later.

### The installer

The installer script that the platform serves (platform spec 186)
downloads `checksums.txt.sig` and verifies it with the same current key,
written into the script, before it checks the archive.

### What `latere devices` needs from this

`latere devices connect` refuses to start on a build whose `keys.go`
holds no key, which is how a development build or an unsigned fork
reads.

## Verification

1. `TestUpgradeVerifiesTheSignature`: a release served by a stub with a
   good signature upgrades, and one with a missing, wrong or truncated
   signature changes nothing and says why.
2. `TestNextKeyVerifies` and `TestOldKeyGone`: either built-in key
   verifies, and a key removed from `keys.go` does not.
3. `TestNoDowngradeToUnsigned`.
4. The release workflow's dry run produces `checksums.txt.sig`, and a
   test in CI verifies it with the public key in `keys.go`.

## Outcome

Built on 2026-10-18, not released. Verification items 1 to 3 have passing
tests under the names above; item 4 is met differently, as described below.
The installer's half is not built here.

- **Signing.** GoReleaser's `signs` runs `go run ./tools/sign` over
  `checksums.txt`. The signer is standard library only, beside
  `internal/upgrade`. It reads the private key from `RELEASE_SIGNING_KEY`,
  the standard base64 of a 32-byte Ed25519 seed, and writes
  `checksums.txt.sig` as the standard base64 of the 64-byte signature, with
  no newline. It fails, and the release with it, when the variable is unset
  or malformed, or when `keys.go` does not hold its public half, so a
  release can carry only a signature its own CLI accepts. `-keygen` writes a
  new private key to a file only its owner can read and prints the public
  key, which is how a rotation starts; CONTRIBUTING.md has the steps.
- **The secret.** `RELEASE_SIGNING_KEY` is a repository secret, not a
  secret of a release environment as Design says: the shared pipeline's job
  names no environment. `release.yaml` is unchanged. Its `secrets: inherit`
  already passes the secret, and `cli-release.yml` in `latere-ai/ci` now
  declares it optional and sets it in the GoReleaser step's environment
  alone.
- **Verifying.** `DownloadBinary` fetches `checksums.txt` and
  `checksums.txt.sig` first, verifies the exact bytes of the checksums, and
  only then downloads the archive. A missing signature (a 404), one that is
  not base64, one that is not 64 bytes, and one no built-in key verifies all
  refuse with the one sentence. The reason stays in the error's detail.
  Any other failure to download the signature is reported as a download
  failure, since the release may well be signed. `latere upgrade` returns
  the sentence alone, without the "does that release exist?" hint other
  failures carry.
- **Auto-upgrade** prints the same sentence after its "Auto-upgrading"
  line and records the refused release in `update-check.json`. It tries
  that release again after a day, not on every command. That limits the
  repeated network calls and messages, and it recovers from a refusal seen
  while a new release was still uploading its signature.
- **No downgrade.** `firstSignedRelease` is `v0.21.0`. An install of an
  older release, by version or because the latest release is older, is
  refused before anything is downloaded. The constant has to name the first
  release tagged with this change.
- **For `latere devices`.** `upgrade.HasReleaseKey` reports whether the
  build holds a well-formed key. Because the real public key is committed,
  a build from source holds it too; only a fork that empties `keys.go`
  reads as keyless.
- **Item 4.** The pipeline has no dry run in CI. Instead the signer checks
  its key against `keys.go` on every release, and `tools/sign`'s tests sign
  a fixture with a throwaway key and verify it with the CLI's own
  `VerifySignature`. A local GoReleaser snapshot run produced
  `checksums.txt.sig` with a throwaway key and stopped the release when the
  key was unset or not in `keys.go`. The end-to-end tests serve releases
  signed by a throwaway key to the test binary through
  `SetReleaseKeysForTest`, which nothing in the CLI calls.
- **Not built here.** The installer script belongs to
  `latere-ai/platform`, spec 186, and does not verify a signature yet.
  Until it does, the installer checks only the checksums.

### Live, 2026-10-18

v0.21.0 was the first signed release. Its `checksums.txt.sig` verifies with
the public key in `keys.go` by a separate Ed25519 check, the darwin arm64
archive matches its checksum, the v0.21.0 binary reinstalls itself
(`latere upgrade v0.21.0`), and an upgrade to the unsigned v0.20.1 is
refused. v0.22.1 verifies the same way. The installer's check is still to
come.

### The installer's signature, 2026-10-19

Stock macOS has no raw Ed25519 verifier (its LibreSSL lacks one), so the
installer cannot check `checksums.txt.sig` as Design says. It can run
`ssh-keygen -Y verify`, which macOS ships and Linux has from OpenSSH 8.1, so
each release from the next one also carries `checksums.txt.sshsig`: the
same key's signature of `checksums.txt` in OpenSSH's signature format
(PROTOCOL.sshsig), with the namespace `latere-release` and SHA-512, armored
as `ssh-keygen -Y sign` writes it.

- `tools/sign -sshsig <file> <signature-file>` writes it, from the same
  `RELEASE_SIGNING_KEY` and with the same check against `keys.go`. The
  signer stays standard library only: the few SSH wire strings are written
  by hand. For one key and one file its output is byte for byte what
  `ssh-keygen -Y sign -n latere-release` writes.
- `.goreleaser.yaml` has a second `signs` entry, `checksums-ssh`, since
  GoReleaser uploads the one file each entry names; a local snapshot run
  published `checksums.txt.sig` and `checksums.txt.sshsig`, and the second
  verified with `ssh-keygen`.
- `upgrade.AllowedSigners` derives the allowed signers line from
  `keys.go`, and `TestAllowedSignersLine` holds it to the current key. The
  installer embeds
  `latere-release ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMfnPqHvaCgoT2+x/5xSoSAgrbXaY2Zszk2gReHc1nPg`
  and runs `ssh-keygen -Y verify -f <allowed_signers> -I latere-release -n
  latere-release -s checksums.txt.sshsig < checksums.txt`. A rotation adds
  the next key's line there as it adds the key to `keys.go`.
- `TestSSHSignatureVerifiesWithSSHKeygen` signs a fixture with a throwaway
  key and verifies it with the real `ssh-keygen`, which refuses other
  content, another namespace and another key; it skips where `ssh-keygen`
  is not on `PATH`. `TestGoReleaserPublishesBothSignatures` holds the two
  `signs` entries.
- `checksums.txt.sig` and `latere upgrade`'s check are unchanged.

The installer's own half shipped in `latere-ai/platform` v0.148.0: `curl
-fsSL https://platform.latere.ai | sh` verified v0.24.2's signature on
2026-10-19 before installing it.

### The notice names no refused release, 2026-10-19

The upgrade notice could name a release the CLI had refused for its
signature. A run whose auto-upgrade refused a release printed the refusal
and then "A new release of latere is available ... Run `latere upgrade` to
update.", and every day after it repeated that notice, while `latere
upgrade` refused the release each time. `latere upgrade` did not record its
own refusal, so with auto-upgrade off the notice named the release daily.

- `shouldNotify` is false for the release `update-check.json` records as
  refused. A newer release than the refused one is announced as before.
- `latere upgrade` records a refusal in `update-check.json` the way
  auto-upgrade does, through the one `recordRefusal`.
- `latere upgrade --check` on a release recorded as refused says "It was
  refused when last installed: it is not signed by Latere." in place of
  "Run `latere upgrade` to update."
- Auto-upgrade still tries a refused release again after `refusalRetry`,
  which recovers from a refusal seen while a release was still uploading
  its signature; only the notice stays silent until then and after.
- `TestNoticeNeverNamesARefusedRelease` holds the refusing run, a later run
  with auto-upgrade off and a newer release after the refused one;
  `TestUpgradeRecordsARefusal` holds `latere upgrade`'s record and the
  check's line. Both fail on the code before this change.
