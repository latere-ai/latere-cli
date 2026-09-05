// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSaveTokenReplacesFilePrivately(t *testing.T) {
	for _, auth := range []bool{false, true} {
		name := "cella"
		if auth {
			name = "auth"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token.json")
			t.Setenv("LATERE_TOKEN_FILE", path)
			t.Setenv("LATERE_AUTH_TOKEN_FILE", path)
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
			if auth {
				err = SaveAuthToken(want)
			} else {
				err = SaveToken("", want)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := LoadToken(path)
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
		})
	}
}

func TestSaveTokenFailurePreservesPreviousToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "token.json")
	want := Token{AccessToken: "test-token"}
	if err := SaveToken(path, want); err != nil {
		t.Fatal(err)
	}
	if err := SaveToken(path, Token{ExpiresAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Fatal("invalid timestamp unexpectedly saved")
	}
	if got, err := LoadToken(path); err != nil || got != want {
		t.Fatalf("previous token lost: %v", err)
	}
	if err := SaveToken(filepath.Join(path, "child"), want); err == nil {
		t.Fatal("non-directory parent accepted")
	}
	if err := SaveToken(filepath.Dir(path), want); err == nil {
		t.Fatal("directory destination accepted")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "token.json" {
		t.Fatal("failed save left temporary files")
	}
}

func TestSaveTokenReplacesSymlinkWithoutChangingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	path := filepath.Join(root, "token.json")
	old := `{"access_token":"old-test-token"}`
	if err := os.WriteFile(target, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := SaveToken(path, Token{AccessToken: "new-test-token"}); err != nil {
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
