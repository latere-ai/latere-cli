package upgrade

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReplaceFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "latere")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(target, []byte("NEW-BINARY")); err != nil {
		t.Fatalf("replaceFile: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW-BINARY" {
		t.Errorf("contents = %q, want NEW-BINARY", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Errorf("replaced binary is not executable: %v", info.Mode())
	}
	// The temp file must not linger.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1 (temp file left behind)", len(entries))
	}
}

func TestReplaceFileNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions not enforced for this platform/user")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "latere")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // read+exec, no write
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700) //nolint:errcheck // cleanup

	err := replaceFile(target, []byte("NEW"))
	if err == nil {
		t.Fatal("expected a permission error on a non-writable directory")
	}
	// The message should guide the user back to the installer.
	if !containsAll(err.Error(), "install.sh") {
		t.Errorf("error %q should mention install.sh", err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
