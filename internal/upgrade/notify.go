// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"context"
	"errors"
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

// checkTimeout bounds the release check that runs before a command when one
// is due. The check takes a few hundred milliseconds; a black-holed network
// must not stall the command for longer than this.
const checkTimeout = 2 * time.Second

// OnStart runs the release check from the root command's PersistentPreRunE.
// It is best-effort and never returns an error: a failed check must not
// block the user's command.
//
// Once per checkInterval, on an interactive run, it asks for the latest
// release before the command. When one is newer, a CLI that upgrades itself
// replaces the binary and re-execs the original command on it in that same
// run; otherwise it prints a notice. A failed check is not repeated for
// failureBackoff, so an offline machine is not slowed on every command.
func OnStart(current string, w io.Writer) {
	if !isRelease(current) || disabled() || !isTerminalStderr() {
		return
	}
	now := time.Now()
	st := loadState()
	auto := autoUpgradeWanted()
	// A newer release found earlier but not installed, as after a failed
	// download, is asked for again so the retry starts from a fresh answer.
	pending := auto && Newer(current, st.LatestVersion) && !st.refusedRecently(st.LatestVersion, now)
	answered := false
	if (stale(st, now) || pending) && !st.backingOff(now) {
		st, answered = check(st, now)
	}
	if !Newer(current, st.LatestVersion) {
		return
	}
	// Only this run's answer starts an upgrade, never the cache, so an
	// offline run does not start a download it cannot finish.
	if auto && answered && !st.refusedRecently(st.LatestVersion, now) {
		// performAutoUpgrade re-execs on success and does not return.
		performAutoUpgrade(current, st.LatestVersion, w)
		st = loadState() // keep a refusal it recorded
	}
	if st.shouldNotify(st.LatestVersion, now) {
		printNotice(w, current, st.LatestVersion)
		// Record the reminder so it repeats at most once per interval rather
		// than on every command until the user upgrades.
		st.NotifiedVersion = st.LatestVersion
		st.NotifiedAt = now
		_ = saveState(st)
	}
}

// autoUpgradeWanted reports whether an auto-upgrade should run now: on by
// default (overridable), supported and writable on this platform, and not
// already inside a post-upgrade re-exec.
func autoUpgradeWanted() bool {
	return LoadConfig().AutoUpgradeEnabled() &&
		replaceSupported() &&
		selfReplaceWritable() &&
		os.Getenv(sentinelEnv) == ""
}

// check asks GitHub for the latest release and records the answer, or the
// time of the failure so the next runs back off. It reports whether it got
// an answer.
func check(st checkState, now time.Time) (checkState, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	tag, err := ResolveLatest(ctx, httpClient())
	if err != nil {
		st.FailedAt = now
		_ = saveState(st)
		return st, false
	}
	st.CheckedAt, st.LatestVersion, st.FailedAt = now, tag, time.Time{}
	_ = saveState(st)
	return st, true
}

func printNotice(w io.Writer, current, latest string) {
	fprintf(w, "\nA new release of latere is available: %s -> %s\nRun `latere upgrade` to update.\n",
		display(current), display(latest))
}

// performAutoUpgrade installs tag and re-execs the original command on it.
// On any failure it prints a short note and returns so the current command
// can still run. A release refused for its signature is recorded, so it is
// tried again after refusalRetry rather than on every command.
func performAutoUpgrade(current, tag string, w io.Writer) {
	fprintf(w, "Auto-upgrading latere %s -> %s...\n", display(current), display(tag))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	bin, err := DownloadBinary(ctx, downloadClient(), tag)
	if _, ok := errors.AsType[*unsignedError](err); ok {
		fprintln(w, err)
		recordRefusal(tag, time.Now())
		return
	}
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
		st.CheckedAt, st.LatestVersion, st.FailedAt = time.Now(), tag, time.Time{}
		_ = saveState(st)
	}

	// For the latest path, a non-release (dev) build is always treated as
	// behind so an explicit `latere upgrade` on a local build still installs
	// the published release. An explicit target is always installed (that is
	// the whole point of a rollback).
	atLatest := target == "" && isRelease(current) && !Newer(current, tag)
	if atLatest {
		if _, err := fmt.Fprintf(out, "latere %s is already the latest release.\n", display(current)); err != nil {
			return fmt.Errorf("write upgrade check result: %w", err)
		}
		return nil
	}
	if checkOnly {
		hint := "Run `latere upgrade` to update."
		if loadState().RefusedVersion == tag {
			hint = "It was refused when last installed: it is not signed by Latere."
		}
		if _, err := fmt.Fprintf(out, "A new release of latere is available: %s -> %s\n%s\n",
			display(current), display(tag), hint); err != nil {
			return fmt.Errorf("write upgrade check result: %w", err)
		}
		return nil
	}
	if _, err := releaseKeysFor(tag); err != nil {
		return err
	}
	if !replaceSupported() {
		return fmt.Errorf("in-place upgrade is not supported on this platform; "+
			"download latere %s from https://github.com/%s/releases", display(tag), repoSlug)
	}
	if _, err := fmt.Fprintf(out, "%s latere %s...\n", installVerb(current, tag), display(tag)); err != nil {
		return fmt.Errorf("write upgrade progress: %w", err)
	}
	bin, err := DownloadBinary(ctx, downloadClient(), tag)
	if _, ok := errors.AsType[*unsignedError](err); ok {
		recordRefusal(tag, time.Now())
		return err
	}
	if err != nil {
		return fmt.Errorf("install latere %s: %w (does that release exist? see https://github.com/%s/releases)",
			display(tag), err, repoSlug)
	}
	if err := replace(bin); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Now on latere %s.\n", display(tag)); err != nil {
		return fmt.Errorf("latere %s was installed, but writing confirmation failed: %w", display(tag), err)
	}
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
