// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package upgrade

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// signedRelease serves release tag at a test server: its archive holding
// binary, its checksums.txt, and the signature sig returns for those
// checksums (status 404 when sig returns ""). It records every path asked
// for, and points githubBase at the server for the rest of the test.
type signedRelease struct {
	mu        sync.Mutex
	requested []string
}

func serveSignedRelease(t *testing.T, tag, binary string, sig func(sums string) string) *signedRelease {
	t.Helper()
	archive := makeTarGz(t, map[string]string{"latere": binary})
	sum := sha256.Sum256(archive)
	asset := assetName(tag)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)
	release := &signedRelease{}
	prefix := "/" + repoSlug + "/releases/download/" + tag + "/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release.mu.Lock()
		release.requested = append(release.requested, r.URL.Path)
		release.mu.Unlock()
		switch r.URL.Path {
		case "/" + repoSlug + "/releases/latest":
			http.Redirect(w, r, "/"+repoSlug+"/releases/tag/"+tag, http.StatusFound)
		case prefix + asset:
			_, _ = w.Write(archive)
		case prefix + "checksums.txt":
			_, _ = fmt.Fprint(w, sums)
		case prefix + "checksums.txt.sig":
			body := sig(sums)
			if body == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := githubBase
	githubBase = srv.URL
	t.Cleanup(func() { githubBase = old })
	return release
}

// paths returns the paths asked for so far.
func (r *signedRelease) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requested...)
}

// downloadedAny reports whether any of the release's files was asked for,
// as opposed to only the latest-release lookup.
func (r *signedRelease) downloadedAny() bool {
	for _, p := range r.paths() {
		if strings.Contains(p, "/releases/download/") {
			return true
		}
	}
	return false
}

// downloadedArchive reports whether the release archive was asked for.
func (r *signedRelease) downloadedArchive() bool {
	for _, p := range r.paths() {
		if strings.HasSuffix(p, ".tar.gz") || strings.HasSuffix(p, ".zip") {
			return true
		}
	}
	return false
}

// requireUnsigned fails unless err is the signature refusal, reading as the
// one sentence the spec fixes, with a detail that contains want.
func requireUnsigned(t *testing.T, err error, want string) {
	t.Helper()
	refused, ok := errors.AsType[*unsignedError](err)
	if !ok {
		t.Fatalf("error = %v, want the signature refusal", err)
	}
	if err.Error() != "this release is not signed by Latere; nothing was changed" {
		t.Errorf("message = %q", err.Error())
	}
	if !strings.Contains(refused.detail, want) {
		t.Errorf("detail = %q, want it to contain %q", refused.detail, want)
	}
}

func TestUpgradeVerifiesTheSignature(t *testing.T) {
	_, other := newTestKey(t)
	for _, tc := range []struct {
		name   string
		sig    func(private ed25519.PrivateKey, sums string) string
		detail string // empty: the upgrade verifies
	}{
		{"good", func(p ed25519.PrivateKey, sums string) string { return signSums(p, sums) }, ""},
		{"good with a trailing newline", func(p ed25519.PrivateKey, sums string) string { return signSums(p, sums) + "\n" }, ""},
		{"missing", func(ed25519.PrivateKey, string) string { return "" }, "has no checksums.txt.sig"},
		{"wrong key", func(_ ed25519.PrivateKey, sums string) string { return signSums(other, sums) }, "verifies against none of the 1 release keys"},
		{"other content", func(p ed25519.PrivateKey, sums string) string { return signSums(p, sums+"extra") }, "verifies against none"},
		{"truncated", func(p ed25519.PrivateKey, sums string) string { return signSums(p, sums)[:44] }, "holds 33 bytes"},
		{"not base64", func(p ed25519.PrivateKey, sums string) string { return "!" + signSums(p, sums)[1:] }, "not standard base64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			private := useTestKey(t)
			release := serveSignedRelease(t, "v9.9.9", "NEW-BINARY", func(sums string) string { return tc.sig(private, sums) })

			got, err := DownloadBinary(context.Background(), nil, "v9.9.9")
			if tc.detail == "" {
				if err != nil || string(got) != "NEW-BINARY" {
					t.Fatalf("DownloadBinary = %q, %v; want the binary", got, err)
				}
				return
			}
			requireUnsigned(t, err, tc.detail)
			if got != nil || release.downloadedArchive() {
				t.Errorf("a refused release returned %q and downloaded its archive: %v", got, release.paths())
			}

			// `latere upgrade` refuses with the sentence alone, with no
			// installation hint after it, and prints nothing more.
			var out bytes.Buffer
			err = Run(context.Background(), "v1.0.0", "v9.9.9", false, &out)
			if runtime.GOOS == "windows" {
				return
			}
			requireUnsigned(t, err, tc.detail)
			if out.String() != "Upgrading to latere v9.9.9...\n" {
				t.Errorf("output = %q", out.String())
			}
		})
	}
}

// A signature download that fails for a reason other than a missing file is
// a network failure, not a refusal: the release may well be signed.
func TestSignatureDownloadFailureIsNotARefusal(t *testing.T) {
	useTestKey(t)
	archive := makeTarGz(t, map[string]string{"latere": "NEW-BINARY"})
	sum := sha256.Sum256(archive)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			_, _ = fmt.Fprintf(w, "%x  %s\n", sum, assetName("v9.9.9"))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	old := githubBase
	githubBase = srv.URL
	defer func() { githubBase = old }()

	_, err := DownloadBinary(context.Background(), srv.Client(), "v9.9.9")
	if _, refused := errors.AsType[*unsignedError](err); refused || err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("error = %v, want the download failure", err)
	}
}

func TestNextKeyVerifies(t *testing.T) {
	currentPublic, current := newTestKey(t)
	nextPublic, next := newTestKey(t)
	t.Cleanup(SetReleaseKeysForTest(currentPublic, nextPublic))
	for name, signer := range map[string]ed25519.PrivateKey{"current": current, "next": next} {
		t.Run(name, func(t *testing.T) {
			serveSignedRelease(t, "v9.9.9", "NEW-BINARY", func(sums string) string { return signSums(signer, sums) })
			if got, err := DownloadBinary(context.Background(), nil, "v9.9.9"); err != nil || string(got) != "NEW-BINARY" {
				t.Fatalf("DownloadBinary = %q, %v; want the binary", got, err)
			}
		})
	}
}

func TestOldKeyGone(t *testing.T) {
	_, old := newTestKey(t)
	newPublic, _ := newTestKey(t)
	t.Cleanup(SetReleaseKeysForTest(newPublic))
	release := serveSignedRelease(t, "v9.9.9", "NEW-BINARY", func(sums string) string { return signSums(old, sums) })
	_, err := DownloadBinary(context.Background(), nil, "v9.9.9")
	requireUnsigned(t, err, "verifies against none of the 1 release keys")
	if release.downloadedArchive() {
		t.Errorf("archive downloaded for a release signed by a removed key: %v", release.paths())
	}
}

func TestNoDowngradeToUnsigned(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	private := useTestKey(t)
	// The fixture is signed, so only the version keeps it from installing.
	release := serveSignedRelease(t, "v0.20.1", "OLD-BINARY", func(sums string) string { return signSums(private, sums) })

	var out bytes.Buffer
	err := Run(context.Background(), "v0.21.0", "v0.20.1", false, &out)
	requireUnsigned(t, err, "v0.20.1 is older than v0.21.0, the first signed release")
	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing before the refusal", out.String())
	}
	_, err = DownloadBinary(context.Background(), nil, "v0.20.1")
	requireUnsigned(t, err, "older than v0.21.0")

	// A development build installing the latest release is refused the same
	// way when the latest release is older than the first signed one.
	out.Reset()
	err = Run(context.Background(), "dev", "", false, &out)
	requireUnsigned(t, err, "v0.20.1 is older than v0.21.0")
	for _, p := range release.paths() {
		if strings.Contains(p, "/releases/download/") {
			t.Errorf("an unsigned release was downloaded: %s", p)
		}
	}

	if _, err := releaseKeysFor(firstSignedRelease); err != nil {
		t.Errorf("the first signed release is refused: %v", err)
	}
}

func TestNoReleaseKeyRefusesEveryUpgrade(t *testing.T) {
	t.Cleanup(SetReleaseKeysForTest())
	if HasReleaseKey() {
		t.Fatal("HasReleaseKey with no key")
	}
	release := serveSignedRelease(t, "v9.9.9", "NEW-BINARY", func(string) string { return "" })
	_, err := DownloadBinary(context.Background(), nil, "v9.9.9")
	requireUnsigned(t, err, "holds no release key")
	if len(release.paths()) != 0 {
		t.Errorf("a build with no key downloaded %v", release.paths())
	}
}

func TestReleaseKeysAreWellFormed(t *testing.T) {
	keys, err := PublicKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 || len(keys) > 2 {
		t.Fatalf("keys.go holds %d release keys; want the current key and at most a next one", len(keys))
	}
	if len(keys) == 2 && keys[0].Equal(keys[1]) {
		t.Error("the current and next release keys are the same key")
	}
	if !HasReleaseKey() {
		t.Error("HasReleaseKey is false for a build that holds a key")
	}
}

func TestPublicKeysRejectsMalformedKeys(t *testing.T) {
	a, _ := newTestKey(t)
	b, _ := newTestKey(t)
	c, _ := newTestKey(t)
	encode := base64.StdEncoding.EncodeToString
	for _, tc := range []struct {
		name string
		keys []string
		want string
	}{
		{"three keys", []string{encode(a), encode(b), encode(c)}, "at most 2"},
		{"not base64", []string{"not base64!"}, "not standard base64"},
		{"short", []string{encode(a[:31])}, "holds 31 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := releaseKeys
			releaseKeys = tc.keys
			t.Cleanup(func() { releaseKeys = previous })
			if _, err := PublicKeys(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("PublicKeys error = %v, want %q", err, tc.want)
			}
			if HasReleaseKey() {
				t.Error("HasReleaseKey with a malformed key")
			}
			if _, err := releaseKeysFor("v9.9.9"); err == nil {
				t.Error("a build with a malformed key would upgrade")
			}
		})
	}
}

func TestVerifySignatureRejectsAMalformedKey(t *testing.T) {
	public, private := newTestKey(t)
	sig := []byte(signSums(private, "sums"))
	if err := VerifySignature([]byte("sums"), sig, []ed25519.PublicKey{public[:16]}); err == nil || !strings.Contains(err.Error(), "holds 16 bytes") {
		t.Errorf("error = %v, want the malformed key named", err)
	}
	if err := VerifySignature([]byte("sums"), sig, nil); err == nil {
		t.Error("verified against no key")
	}
}

func TestAutoUpgradeRecordsUnsignedRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("auto-upgrade does not run on Windows")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	useTestKey(t)
	release := serveSignedRelease(t, "v9.9.9", "NEW-BINARY", func(string) string { return "" })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	performAutoUpgrade("v1.0.0", "v9.9.9", &out)
	want := "Auto-upgrading latere v1.0.0 -> v9.9.9...\nthis release is not signed by Latere; nothing was changed\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
	if st := loadState(); st.RefusedVersion != "v9.9.9" || time.Since(st.RefusedAt) > time.Minute {
		t.Errorf("state = %+v, want the refusal of v9.9.9 recorded", st)
	}
	after, err := os.ReadFile(executable)
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("the executable changed (error %v)", err)
	}
	if release.downloadedArchive() {
		t.Errorf("archive downloaded for an unsigned release: %v", release.paths())
	}
}

// A refused release is tried again once a day, not on every command.
func TestOnStartSkipsARecentlyRefusedRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("auto-upgrade does not run on Windows")
	}
	t.Setenv("CI", "")
	t.Setenv(envNoCheck, "")
	t.Setenv(sentinelEnv, "")
	old := isTerminalStderr
	isTerminalStderr = func() bool { return true }
	t.Cleanup(func() { isTerminalStderr = old })
	useTestKey(t)
	for _, tc := range []struct {
		name      string
		refusedAt time.Duration // how long ago
		retried   bool
	}{
		{"refused an hour ago", time.Hour, false},
		{"refused a day ago", refusalRetry + time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			release := serveSignedRelease(t, "v9.9.9", "NEW-BINARY", func(string) string { return "" })
			now := time.Now()
			if err := saveState(checkState{CheckedAt: now, LatestVersion: "v9.9.9", RefusedVersion: "v9.9.9", RefusedAt: now.Add(-tc.refusedAt)}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			OnStart("v1.0.0", &out)
			retried := release.downloadedAny()
			if retried != tc.retried {
				t.Errorf("auto-upgrade retried = %v, want %v (requests %v, output %q)", retried, tc.retried, release.paths(), out.String())
			}
			if strings.Contains(out.String(), unsignedMessage) != tc.retried {
				t.Errorf("output = %q", out.String())
			}
		})
	}
}

// A release refused for its signature is not announced as one to install:
// not on the run that refused it, and not on a later run, with auto-upgrade
// on or off. A newer release after it is announced.
func TestNoticeNeverNamesARefusedRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("auto-upgrade does not run on Windows")
	}
	onStartEnv(t)
	useTestKey(t)
	serveSignedRelease(t, "v9.9.9", "NEW-BINARY", func(string) string { return "" })
	if err := saveState(checkState{CheckedAt: time.Now().Add(-checkInterval), LatestVersion: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	OnStart("v1.0.0", &out)
	if !strings.Contains(out.String(), unsignedMessage) || strings.Contains(out.String(), "A new release") {
		t.Errorf("the refusing run: output %q, want the refusal and no notice", out.String())
	}

	off := false
	if err := SaveConfig(Config{AutoUpgrade: &off}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, tc := range []struct {
		name, latest, want string
	}{
		{"the refused release, days later", "v9.9.9", ""},
		{"a newer release", "v9.9.10", "v1.0.0 -> v9.9.10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := saveState(checkState{
				CheckedAt: now, LatestVersion: tc.latest,
				NotifiedVersion: "v9.9.9", NotifiedAt: now.Add(-3 * noticeInterval),
				RefusedVersion: "v9.9.9", RefusedAt: now.Add(-3 * refusalRetry),
			}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			OnStart("v1.0.0", &out)
			if tc.want == "" && out.Len() != 0 || tc.want != "" && !strings.Contains(out.String(), tc.want) {
				t.Errorf("output %q, want %q", out.String(), tc.want)
			}
		})
	}
}

// `latere upgrade` records a refusal as auto-upgrade does, so the notice
// stops naming the release, and its check then says the release was
// refused rather than to install it.
func TestUpgradeRecordsARefusal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("in-place upgrade does not run on Windows")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	useTestKey(t)
	serveSignedRelease(t, "v9.9.9", "NEW-BINARY", func(string) string { return "" })

	var out bytes.Buffer
	requireUnsigned(t, Run(context.Background(), "v1.0.0", "", false, &out), "has no checksums.txt.sig")
	if st := loadState(); st.RefusedVersion != "v9.9.9" || time.Since(st.RefusedAt) > time.Minute {
		t.Errorf("state = %+v, want the refusal of v9.9.9 recorded", st)
	}

	out.Reset()
	if err := Run(context.Background(), "v1.0.0", "", true, &out); err != nil {
		t.Fatalf("Run --check: %v", err)
	}
	want := "A new release of latere is available: v1.0.0 -> v9.9.9\nIt was refused when last installed: it is not signed by Latere.\n"
	if out.String() != want {
		t.Errorf("check output = %q, want %q", out.String(), want)
	}
}

func TestRefusedRecently(t *testing.T) {
	now := time.Now()
	st := checkState{RefusedVersion: "v9.9.9", RefusedAt: now.Add(-time.Hour)}
	if !st.refusedRecently("v9.9.9", now) {
		t.Error("a refusal an hour ago is not recent")
	}
	if st.refusedRecently("v9.9.10", now) {
		t.Error("a refusal of another release holds back a newer one")
	}
	if st.refusedRecently("v9.9.9", now.Add(refusalRetry)) {
		t.Error("a refusal is still recent after a day")
	}
}
