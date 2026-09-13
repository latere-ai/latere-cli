// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// setTokenPath points the one credential file at p.
func setTokenPath(t *testing.T, p string) {
	t.Helper()
	t.Setenv("LATERE_AUTH_TOKEN_FILE", p)
}

func TestSaveAuthTokenReplacesFilePrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-token.json")
	setTokenPath(t, path)
	old := `{"access_token":"old-test-token"}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	want := Token{AccessToken: "new-test-token", RefreshToken: "test-refresh", ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := SaveAuthToken(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAuthToken()
	if err != nil || got != want {
		t.Fatalf("saved token did not round-trip: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Errorf("token permissions = %04o, want 0600", info.Mode().Perm())
	}
	// An already-open reader must retain the complete previous version;
	// truncating its inode exposes partial JSON to concurrent CLI processes.
	before, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != old {
		t.Error("saving a token modified an existing reader's file")
	}
}

func TestSaveAuthTokenFailurePreservesPreviousToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "auth-token.json")
	setTokenPath(t, path)
	want := Token{AccessToken: "test-token"}
	if err := SaveAuthToken(want); err != nil {
		t.Fatal(err)
	}
	if err := SaveAuthToken(Token{ExpiresAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Fatal("invalid timestamp unexpectedly saved")
	}
	if got, err := LoadAuthToken(); err != nil || got != want {
		t.Fatalf("previous token lost: %v", err)
	}
	setTokenPath(t, filepath.Join(path, "child"))
	if err := SaveAuthToken(want); err == nil {
		t.Fatal("non-directory parent accepted")
	}
	setTokenPath(t, filepath.Dir(path))
	if err := SaveAuthToken(want); err == nil {
		t.Fatal("directory destination accepted")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "auth-token.json" {
		t.Fatal("failed save left temporary files")
	}
}

func TestSaveAuthTokenReplacesSymlinkWithoutChangingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	path := filepath.Join(root, "auth-token.json")
	setTokenPath(t, path)
	old := `{"access_token":"old-test-token"}`
	if err := os.WriteFile(target, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := SaveAuthToken(Token{AccessToken: "new-test-token"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatal("token path was not replaced with a regular file")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != old {
		t.Fatal("saving token overwrote the symlink target")
	}
}

// The login token is the only credential kept on disk. A product token is
// minted for one call and held in memory; writing one beside the login
// would put a second, longer-lived credential on the filesystem.
func TestOnlyTheLoginTokenIsWrittenToDisk(t *testing.T) {
	dir := t.TempDir()
	setTokenPath(t, filepath.Join(dir, "auth-token.json"))
	if err := SaveAuthToken(Token{AccessToken: "login-access"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "auth-token.json" {
		t.Fatalf("token directory holds %v, want auth-token.json alone", entries)
	}
}

func TestLoadAuthTokenReportsAbsenceAsErrNoToken(t *testing.T) {
	setTokenPath(t, filepath.Join(t.TempDir(), "absent.json"))
	if _, err := LoadAuthToken(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}
