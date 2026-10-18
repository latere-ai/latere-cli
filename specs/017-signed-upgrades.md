---
title: "Signed upgrades: each release signs its checksums with an Ed25519 key held by the release pipeline, and `latere upgrade`, auto-upgrade and the installer verify the signature against keys built into the binary before trusting any checksum"
status: in-progress
depends_on:
  - "CROSS-REPO: latere-ai/platform specs/186-the-latere-command-installs-from-the-platforms-address.md, the installer that verifies the same signature"
affects:
  - .goreleaser.yaml (`signs` over `checksums.txt`)
  - .github/workflows/release.yaml (the signing key from the repository's secrets)
  - internal/upgrade/release.go (verify before `checksumFor`)
  - internal/upgrade/keys.go (new: the public keys)
  - "CROSS-REPO: latere-ai/platform (the installer script verifies `checksums.txt.sig`)"
  - docs/configuration.md, CHANGELOG.md
effort: small
created: 2026-10-18
updated: 2026-10-18
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
