// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
