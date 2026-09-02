// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"
)

// sentinelEnv is set on the re-exec'd process after an auto-upgrade so it does
// not try to upgrade itself again (which would loop if anything went wrong).
const sentinelEnv = "LATERE_UPGRADED"

// envNoCheck disables all release awareness when set (any value).
const envNoCheck = "LATERE_NO_UPDATE_CHECK"

// disabled reports whether release awareness should be entirely skipped:
// explicitly turned off, or running in CI where a surprise notice or binary
// swap is unwelcome.
func disabled() bool {
	return os.Getenv(envNoCheck) != "" || os.Getenv("CI") != ""
}

// isTerminalStderr reports whether os.Stderr is an interactive terminal.
// Notices and auto-upgrades are suppressed otherwise so piped/JSON consumers
// and scripts are never disturbed. It is a var so tests can force the answer.
var isTerminalStderr = func() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// OnStart runs the passive release check from the root command's
// PersistentPreRunE. It is best-effort and never returns an error: a failed
// check must not block the user's command.
//
// Notices are served instantly from the cached check result; the cache is
// refreshed opportunistically in the background so the next invocation is
// aware. When auto-upgrade is enabled and a newer release is already known,
// the binary is replaced and the original command is re-exec'd on it.
func OnStart(current string, w io.Writer) {
	if !isRelease(current) || disabled() {
		return
	}
	now := time.Now()
	st := loadState()
	updateKnown := Newer(current, st.LatestVersion)

	if updateKnown && autoUpgradeWanted() {
		// performAutoUpgrade re-execs on success and does not return.
		performAutoUpgrade(current, w)
	}
	if updateKnown && isTerminalStderr() && st.shouldNotify(st.LatestVersion, now) {
		printNotice(w, current, st.LatestVersion)
		// Record the reminder so it repeats at most once per interval rather
		// than on every command until the user upgrades.
		st.NotifiedVersion = st.LatestVersion
		st.NotifiedAt = now
		_ = saveState(st)
	}
	if stale(st, now) {
		go refresh()
	}
}

// autoUpgradeWanted reports whether an auto-upgrade should run now: on by
// default (overridable), supported and writable on this platform, interactive,
// and not already inside a post-upgrade re-exec.
func autoUpgradeWanted() bool {
	return LoadConfig().AutoUpgradeEnabled() &&
		replaceSupported() &&
		selfReplaceWritable() &&
		isTerminalStderr() &&
		os.Getenv(sentinelEnv) == ""
}

func printNotice(w io.Writer, current, latest string) {
	fprintf(w, "\nA new release of latere is available: %s -> %s\nRun `latere upgrade` to update.\n",
		display(current), display(latest))
}

// performAutoUpgrade resolves the latest release fresh (the cache is only the
// trigger, never the source of truth), installs it, and re-execs the original
// command. On any failure it prints a short note and returns so the current
// command can still run.
func performAutoUpgrade(current string, w io.Writer) {
	// Re-resolve with a tight timeout: this runs synchronously before the
	// user's command, so a black-holed network must not stall it for long.
	resolveCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	tag, err := ResolveLatest(resolveCtx, httpClient())
	cancel()
	if err != nil {
		return // offline or rate-limited; stay on the current version silently
	}
	now := time.Now()
	st := loadState()
	st.CheckedAt, st.LatestVersion = now, tag
	_ = saveState(st)
	if !Newer(current, tag) {
		return // cache was stale; nothing to do
	}
	fprintf(w, "Auto-upgrading latere %s -> %s...\n", display(current), display(tag))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	bin, err := DownloadBinary(ctx, downloadClient(), tag)
	if err != nil {
		fprintf(w, "auto-upgrade failed: %v\n", err)
		return
	}
	if err := replace(bin); err != nil {
		fprintf(w, "auto-upgrade failed: %v\n", err)
		return
	}
	fprintf(w, "Updated to latere %s. Continuing.\n", display(tag))
	if err := reExec(); err != nil {
		fprintln(w, "Restart latere to use the new version.")
	}
}

// refresh updates the cached latest-release view. Best-effort: errors are
// swallowed and retried on the next stale run.
func refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tag, err := ResolveLatest(ctx, httpClient())
	if err != nil {
		return
	}
	st := loadState()
	st.CheckedAt, st.LatestVersion = time.Now(), tag
	_ = saveState(st)
}

// Run executes `latere upgrade`. An empty target means "the latest release";
// a specific target (e.g. "v0.2.29") installs exactly that version, which is
// how a user rolls back from a broken release. With checkOnly it only reports.
func Run(ctx context.Context, current, target string, checkOnly bool, out io.Writer) error {
	var tag string
	if target != "" {
		if !isRelease(target) {
			return fmt.Errorf("invalid version %q; expected a release like v0.2.29", target)
		}
		tag = display(target)
	} else {
		var err error
		tag, err = ResolveLatest(ctx, httpClient())
		if err != nil {
			return fmt.Errorf("check for updates: %w", err)
		}
		// Keep the notice cache in step with what the user just saw.
		st := loadState()
		st.CheckedAt, st.LatestVersion = time.Now(), tag
		_ = saveState(st)
	}

	// For the latest path, a non-release (dev) build is always treated as
	// behind so an explicit `latere upgrade` on a local build still installs
	// the published release. An explicit target is always installed (that is
	// the whole point of a rollback).
	atLatest := target == "" && isRelease(current) && !Newer(current, tag)
	if atLatest {
		fprintf(out, "latere %s is already the latest release.\n", display(current))
		return nil
	}
	if checkOnly {
		fprintf(out, "A new release of latere is available: %s -> %s\nRun `latere upgrade` to update.\n",
			display(current), display(tag))
		return nil
	}
	if !replaceSupported() {
		return fmt.Errorf("in-place upgrade is not supported on this platform; "+
			"download latere %s from https://github.com/%s/releases", display(tag), repoSlug)
	}
	fprintf(out, "%s latere %s...\n", installVerb(current, tag), display(tag))
	bin, err := DownloadBinary(ctx, downloadClient(), tag)
	if err != nil {
		return fmt.Errorf("install latere %s: %w (does that release exist? see https://github.com/%s/releases)",
			display(tag), err, repoSlug)
	}
	if err := replace(bin); err != nil {
		return err
	}
	fprintf(out, "Now on latere %s.\n", display(tag))
	return nil
}

// installVerb describes the transition from current to tag for user output.
func installVerb(current, tag string) string {
	switch {
	case !isRelease(current):
		return "Installing"
	case Newer(current, tag):
		return "Upgrading to"
	case Newer(tag, current):
		return "Downgrading to"
	default:
		return "Reinstalling"
	}
}
