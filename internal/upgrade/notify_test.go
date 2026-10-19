// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrintNotice(t *testing.T) {
	var b bytes.Buffer
	printNotice(&b, "0.2.30", "v0.3.0")
	out := b.String()
	if !strings.Contains(out, "v0.2.30 -> v0.3.0") {
		t.Errorf("notice missing version transition: %q", out)
	}
	if !strings.Contains(out, "latere upgrade") {
		t.Errorf("notice should tell the user to run latere upgrade: %q", out)
	}
}

// redirectServer serves the releases/latest redirect to the given tag.
func redirectServer(t *testing.T, tag string) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+repoSlug+"/releases/tag/"+tag, http.StatusFound)
	}))
	old := githubBase
	githubBase = srv.URL
	return func() {
		githubBase = old
		srv.Close()
	}
}

func TestRunCheckOnlyReportsUpdate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer redirectServer(t, "v9.9.9")()

	var out bytes.Buffer
	if err := Run(context.Background(), "0.1.0", "", true, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "0.1.0 -> v9.9.9") {
		t.Errorf("check output should report the update: %q", out.String())
	}
	// The check should refresh the cache so a later notice matches.
	if got := loadState().LatestVersion; got != "v9.9.9" {
		t.Errorf("cached latest = %q, want v9.9.9", got)
	}
}

func TestRunAlreadyLatest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer redirectServer(t, "v1.0.0")()

	var out bytes.Buffer
	if err := Run(context.Background(), "1.0.0", "", false, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "already the latest") {
		t.Errorf("expected already-latest message, got: %q", out.String())
	}
}

func TestInstallVerb(t *testing.T) {
	cases := []struct {
		current, tag, want string
	}{
		{"0.2.30", "v0.3.0", "Upgrading to"},
		{"0.3.0", "v0.2.29", "Downgrading to"},
		{"0.2.30", "v0.2.30", "Reinstalling"},
		{"dev", "v0.3.0", "Installing"},
	}
	for _, c := range cases {
		if got := installVerb(c.current, c.tag); got != c.want {
			t.Errorf("installVerb(%q, %q) = %q, want %q", c.current, c.tag, got, c.want)
		}
	}
}

func TestRunRejectsInvalidTarget(t *testing.T) {
	var out bytes.Buffer
	err := Run(context.Background(), "0.2.30", "not-a-version", false, &out)
	if err == nil {
		t.Fatal("expected an error for an invalid target version")
	}
}

func TestOnStartPrintsThrottledNotice(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CI", "")         // ensure not treated as CI
	t.Setenv(envNoCheck, "")   // ensure not disabled
	t.Setenv(sentinelEnv, "1") // skip auto-upgrade; isolate the notice path

	old := isTerminalStderr
	isTerminalStderr = func() bool { return true }
	defer func() { isTerminalStderr = old }()

	// Fresh cache (not stale) so OnStart never touches the network.
	if err := saveState(checkState{CheckedAt: time.Now(), LatestVersion: "v9.9.9"}); err != nil {
		t.Fatal(err)
	}

	var first bytes.Buffer
	OnStart("0.1.0", &first)
	if !strings.Contains(first.String(), "v0.1.0 -> v9.9.9") {
		t.Fatalf("first OnStart should print the notice, got: %q", first.String())
	}

	// A second invocation within the interval is throttled.
	var second bytes.Buffer
	OnStart("0.1.0", &second)
	if second.Len() != 0 {
		t.Errorf("second OnStart should be throttled and silent, got: %q", second.String())
	}
}

func TestOnStartSilentForDevBuild(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CI", "")
	t.Setenv(envNoCheck, "")

	old := isTerminalStderr
	isTerminalStderr = func() bool { return true }
	defer func() { isTerminalStderr = old }()

	_ = saveState(checkState{CheckedAt: time.Now(), LatestVersion: "v9.9.9"})

	var out bytes.Buffer
	OnStart("dev", &out)
	if out.Len() != 0 {
		t.Errorf("dev build must never notify, got: %q", out.String())
	}
}

func TestOnStartSilentWhenDisabled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(envNoCheck, "1") // explicitly disabled

	old := isTerminalStderr
	isTerminalStderr = func() bool { return true }
	defer func() { isTerminalStderr = old }()

	_ = saveState(checkState{CheckedAt: time.Now(), LatestVersion: "v9.9.9"})

	var out bytes.Buffer
	OnStart("0.1.0", &out)
	if out.Len() != 0 {
		t.Errorf("disabled check must be silent, got: %q", out.String())
	}
}

// onStartEnv clears what turns the check off and makes stderr a terminal.
func onStartEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CI", "")
	t.Setenv(envNoCheck, "")
	t.Setenv(sentinelEnv, "")
	old := isTerminalStderr
	isTerminalStderr = func() bool { return true }
	t.Cleanup(func() { isTerminalStderr = old })
}

// lookupServer answers the latest-release lookup with status, or with a
// redirect to tag when status is 0, and counts the lookups.
func lookupServer(t *testing.T, tag string, status int) *atomic.Int32 {
	t.Helper()
	var lookups atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		http.Redirect(w, r, "/"+repoSlug+"/releases/tag/"+tag, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	old := githubBase
	githubBase = srv.URL
	t.Cleanup(func() { githubBase = old })
	return &lookups
}

// The run whose check finds a release installs it before the command, not a
// later run.
func TestOnStartUpgradesOnTheRunThatFindsARelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("auto-upgrade does not run on Windows")
	}
	onStartEnv(t)
	useTestKey(t)
	release := serveSignedRelease(t, "v9.9.9", "NEW-BINARY", func(string) string { return "" })
	if err := saveState(checkState{CheckedAt: time.Now().Add(-checkInterval), LatestVersion: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	OnStart("v1.0.0", &out)
	if !strings.Contains(out.String(), "Auto-upgrading latere v1.0.0 -> v9.9.9...") {
		t.Errorf("no auto-upgrade on the run that found the release: %q (requests %v)", out.String(), release.paths())
	}
	if got := loadState().LatestVersion; got != "v9.9.9" {
		t.Errorf("cached latest = %q, want v9.9.9", got)
	}
}

// Within checkInterval of the last check, a run that has nothing to install
// does not ask again.
func TestOnStartChecksOnceADay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("auto-upgrade does not run on Windows")
	}
	onStartEnv(t)
	lookups := lookupServer(t, "v9.9.9", 0)
	if err := saveState(checkState{CheckedAt: time.Now().Add(-checkInterval + time.Hour), LatestVersion: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	OnStart("v1.0.0", &out)
	if n := lookups.Load(); n != 0 || out.Len() != 0 {
		t.Errorf("lookups = %d, output %q; want none", n, out.String())
	}
}

// A release the last check found but did not install, as after a failed
// download, is tried again on the next run from a fresh answer.
func TestOnStartRetriesAFoundRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("auto-upgrade does not run on Windows")
	}
	onStartEnv(t)
	useTestKey(t)
	release := serveSignedRelease(t, "v9.9.9", "NEW-BINARY", func(string) string { return "" })
	if err := saveState(checkState{CheckedAt: time.Now(), LatestVersion: "v9.9.9"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	OnStart("v1.0.0", &out)
	if !strings.Contains(out.String(), "Auto-upgrading latere v1.0.0 -> v9.9.9...") || !release.downloadedAny() {
		t.Errorf("no retry of the found release: %q (requests %v)", out.String(), release.paths())
	}
}

// An offline machine waits out the check once per failureBackoff, not on
// every command.
func TestOnStartBacksOffAfterAFailedCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("auto-upgrade does not run on Windows")
	}
	onStartEnv(t)
	lookups := lookupServer(t, "", http.StatusBadGateway)

	var out bytes.Buffer
	OnStart("v1.0.0", &out)
	OnStart("v1.0.0", &out)
	if n := lookups.Load(); n != 1 {
		t.Errorf("lookups within the backoff = %d, want 1", n)
	}
	if out.Len() != 0 {
		t.Errorf("a failed check printed %q", out.String())
	}

	st := loadState()
	st.FailedAt = time.Now().Add(-failureBackoff - time.Minute)
	if err := saveState(st); err != nil {
		t.Fatal(err)
	}
	OnStart("v1.0.0", &out)
	if n := lookups.Load(); n != 2 {
		t.Errorf("lookups after the backoff = %d, want 2", n)
	}
}

// With auto-upgrade off the check runs once per checkInterval and only
// prints the notice.
func TestOnStartChecksDailyWithoutAutoUpgrade(t *testing.T) {
	onStartEnv(t)
	off := false
	if err := SaveConfig(Config{AutoUpgrade: &off}); err != nil {
		t.Fatal(err)
	}
	lookups := lookupServer(t, "v9.9.9", 0)

	if err := saveState(checkState{CheckedAt: time.Now(), LatestVersion: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	OnStart("v1.0.0", &out)
	if n := lookups.Load(); n != 0 || out.Len() != 0 {
		t.Errorf("fresh cache: lookups = %d, output %q; want none", n, out.String())
	}

	if err := saveState(checkState{CheckedAt: time.Now().Add(-checkInterval), LatestVersion: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	OnStart("v1.0.0", &out)
	if n := lookups.Load(); n != 1 {
		t.Errorf("stale cache: lookups = %d, want 1", n)
	}
	if !strings.Contains(out.String(), "v1.0.0 -> v9.9.9") || strings.Contains(out.String(), "Auto-upgrading") {
		t.Errorf("stale cache: output %q, want the notice alone", out.String())
	}
}

// Scripts and pipes are never slowed by the check.
func TestOnStartSkipsTheCheckWithoutATerminal(t *testing.T) {
	onStartEnv(t)
	isTerminalStderr = func() bool { return false }
	lookups := lookupServer(t, "v9.9.9", 0)

	var out bytes.Buffer
	OnStart("v1.0.0", &out)
	if n := lookups.Load(); n != 0 || out.Len() != 0 {
		t.Errorf("lookups = %d, output %q; want none", n, out.String())
	}
}
