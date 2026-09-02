// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Package upgrade keeps the latere CLI aware of newer GitHub releases and
// can replace the running binary in place.
//
// Release lookup deliberately reads the github.com/<repo>/releases/latest
// redirect rather than api.github.com: the API caps unauthenticated callers
// at 60 requests/hour per IP, a budget routinely exhausted behind shared
// carrier-grade NAT. install.sh resolves "latest" the same way for the same
// reason.
package upgrade

import (
	"strconv"
	"strings"
)

// repoSlug is the GitHub owner/name that hosts the release archives.
const repoSlug = "latere-ai/latere-cli"

type semver struct{ major, minor, patch int }

// parseSemver parses a strict vMAJOR.MINOR.PATCH version, tolerating a
// leading "v" and dropping any -prerelease/+build suffix. Release tags are
// always plain vX.Y.Z, so anything else (e.g. the "dev" default build
// version) is reported as not parseable.
func parseSemver(v string) (semver, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var (
		s   semver
		err error
	)
	if s.major, err = strconv.Atoi(parts[0]); err != nil {
		return semver{}, false
	}
	if s.minor, err = strconv.Atoi(parts[1]); err != nil {
		return semver{}, false
	}
	if s.patch, err = strconv.Atoi(parts[2]); err != nil {
		return semver{}, false
	}
	return s, true
}

// isRelease reports whether v is a real release version (vX.Y.Z) as opposed
// to a local/dev build. Self-update is disabled for non-release builds so
// developers are never nagged or auto-upgraded over their own work.
func isRelease(v string) bool {
	_, ok := parseSemver(v)
	return ok
}

// Newer reports whether candidate is a strictly newer release than current.
// It returns false if either side is not a parseable release version, so a
// "dev" build never counts as older than a published release here.
func Newer(current, candidate string) bool {
	c, ok1 := parseSemver(current)
	n, ok2 := parseSemver(candidate)
	if !ok1 || !ok2 {
		return false
	}
	switch {
	case n.major != c.major:
		return n.major > c.major
	case n.minor != c.minor:
		return n.minor > c.minor
	default:
		return n.patch > c.patch
	}
}

// display normalises a version for user-facing output, adding the leading
// "v" release tags carry. Non-release strings (e.g. "dev") pass through.
func display(v string) string {
	s := strings.TrimSpace(v)
	if !isRelease(s) {
		return s
	}
	return "v" + strings.TrimPrefix(s, "v")
}
