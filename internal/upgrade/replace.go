package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// replaceSupported reports whether in-place self-replacement works on this
// platform. Windows locks the running executable and ships a .zip archive
// rather than .tar.gz, so there the CLI directs users to reinstall instead.
func replaceSupported() bool {
	return runtime.GOOS != "windows"
}

// selfReplaceWritable reports whether the directory holding the running
// binary is writable, i.e. whether an in-place self-replacement could
// succeed. Used to gate auto-upgrade so a system/Homebrew install (not
// user-writable) silently falls back to a notice instead of failing on every
// run.
func selfReplaceWritable() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".latere-perm-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// replace swaps the running executable's file with newBin.
func replace(newBin []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	// Resolve symlinks so we replace the real binary, not a symlink that
	// points at it (e.g. a Homebrew-style shim).
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return replaceFile(exe, newBin)
}

// replaceFile installs newBin at exe. It writes a temp file in the same
// directory (so the rename is atomic on the same filesystem), makes it
// executable, then renames it over the target.
func replaceFile(exe string, newBin []byte) error {
	dir := filepath.Dir(exe)

	tmp, err := os.CreateTemp(dir, ".latere-upgrade-*")
	if err != nil {
		// os.Rename needs write+exec on the containing directory; the
		// target file's own mode is irrelevant. A permission error here is
		// the actionable signal that the install dir is not user-writable.
		if os.IsPermission(err) {
			return notWritableError(dir, exe)
		}
		return fmt.Errorf("prepare upgrade in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if _, err := tmp.Write(newBin); err != nil {
		tmp.Close()
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close new binary: %w", err)
	}
	if err := os.Rename(tmpName, exe); err != nil {
		if os.IsPermission(err) {
			return notWritableError(dir, exe)
		}
		return fmt.Errorf("install new binary: %w", err)
	}
	return nil
}

func notWritableError(dir, exe string) error {
	return fmt.Errorf("cannot write to %s, so %s cannot be replaced.\n"+
		"Re-run the installer:\n"+
		"  curl -fsSL https://latere.ai/install.sh | sh\n"+
		"or, for a system-wide install:\n"+
		"  curl -fsSL https://latere.ai/install.sh | PREFIX=/usr/local sh", dir, exe)
}
