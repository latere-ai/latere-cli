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

// stderrIsTerminal reports whether os.Stderr is an interactive terminal.
// Notices and auto-upgrades are suppressed otherwise so piped/JSON consumers
// and scripts are never disturbed.
func stderrIsTerminal() bool {
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
	st := loadState()
	updateKnown := Newer(current, st.LatestVersion)

	if updateKnown && autoUpgradeWanted() {
		// performAutoUpgrade re-execs on success and does not return.
		performAutoUpgrade(current, w)
	}
	if updateKnown && stderrIsTerminal() {
		printNotice(w, current, st.LatestVersion)
	}
	if stale(st, time.Now()) {
		go refresh()
	}
}

// autoUpgradeWanted reports whether an auto-upgrade should run now: enabled in
// config, supported on this platform, interactive, and not already inside a
// post-upgrade re-exec.
func autoUpgradeWanted() bool {
	return LoadConfig().AutoUpgrade &&
		replaceSupported() &&
		stderrIsTerminal() &&
		os.Getenv(sentinelEnv) == ""
}

func printNotice(w io.Writer, current, latest string) {
	fmt.Fprintf(w, "\nA new release of latere is available: %s -> %s\nRun `latere upgrade` to update.\n",
		display(current), display(latest))
}

// performAutoUpgrade resolves the latest release fresh (the cache is only the
// trigger, never the source of truth), installs it, and re-execs the original
// command. On any failure it prints a short note and returns so the current
// command can still run.
func performAutoUpgrade(current string, w io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tag, err := ResolveLatest(ctx, httpClient())
	if err != nil {
		return // offline or rate-limited; stay on the current version silently
	}
	_ = saveState(checkState{CheckedAt: time.Now(), LatestVersion: tag})
	if !Newer(current, tag) {
		return // cache was stale; nothing to do
	}
	fmt.Fprintf(w, "Auto-upgrading latere %s -> %s...\n", display(current), display(tag))
	bin, err := DownloadBinary(ctx, httpClient(), tag)
	if err != nil {
		fmt.Fprintf(w, "auto-upgrade failed: %v\n", err)
		return
	}
	if err := replace(bin); err != nil {
		fmt.Fprintf(w, "auto-upgrade failed: %v\n", err)
		return
	}
	fmt.Fprintf(w, "Updated to latere %s. Continuing.\n", display(tag))
	if err := reExec(); err != nil {
		fmt.Fprintln(w, "Restart latere to use the new version.")
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
	_ = saveState(checkState{CheckedAt: time.Now(), LatestVersion: tag})
}

// Run executes `latere upgrade`. With checkOnly it only reports; otherwise it
// downloads and installs the latest release when the current build is behind.
func Run(ctx context.Context, current string, checkOnly bool, out io.Writer) error {
	tag, err := ResolveLatest(ctx, httpClient())
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}
	// Keep the notice cache in step with what the user just saw.
	_ = saveState(checkState{CheckedAt: time.Now(), LatestVersion: tag})

	// A non-release (dev) build is always treated as behind so an explicit
	// `latere upgrade` on a local build still installs the published release.
	outdated := Newer(current, tag) || !isRelease(current)
	if !outdated {
		fmt.Fprintf(out, "latere %s is already the latest release.\n", display(current))
		return nil
	}
	if checkOnly {
		fmt.Fprintf(out, "A new release of latere is available: %s -> %s\nRun `latere upgrade` to update.\n",
			display(current), display(tag))
		return nil
	}
	if !replaceSupported() {
		return fmt.Errorf("automatic upgrade is not supported on this platform; "+
			"download the latest release from https://github.com/%s/releases/latest", repoSlug)
	}
	fmt.Fprintf(out, "Upgrading latere %s -> %s...\n", display(current), display(tag))
	bin, err := DownloadBinary(ctx, httpClient(), tag)
	if err != nil {
		return err
	}
	if err := replace(bin); err != nil {
		return err
	}
	fmt.Fprintf(out, "Upgraded to latere %s.\n", display(tag))
	return nil
}
