// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/arca"
)

func TestArcaCommandRegisteredInRoot(t *testing.T) {
	for _, c := range NewRoot("test").Commands() {
		if c.Name() == "arca" {
			return
		}
	}
	t.Fatal("'arca' command not registered in root")
}

func TestArcaVerbSet(t *testing.T) {
	// The command space is deliberately small (specs/003-arca-subcommand.md);
	// growing it should be a conscious spec change, not a arca-by.
	want := map[string]bool{
		"ls": false, "get": false, "put": false, "mv": false, "rm": false,
		"restore": false, "history": false, "share": false, "shares": false, "unshare": false,
	}
	for _, c := range newArcaCmd().Commands() {
		name := c.Name()
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected arca verb %q — update the spec first", name)
			continue
		}
		want[name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing arca verb %q", name)
		}
	}
}

func TestSkipUpdateCheckForArca(t *testing.T) {
	root := NewRoot("test")
	cmd, _, err := root.Find([]string{"arca", "get"})
	if err != nil {
		t.Fatal(err)
	}
	if !skipUpdateCheck(cmd) {
		t.Error("arca subcommands must skip the update check (get -o - streams to stdout)")
	}
}

func TestArcaBearerPrecedence(t *testing.T) {
	t.Setenv("LATERE_AUTH_TOKEN_FILE", "/nonexistent/auth.json")

	t.Run("flag wins", func(t *testing.T) {
		t.Setenv("LATERE_ARCA_TOKEN", "env-tok")
		got, err := arcaBearer(t.Context(), "flag-tok", "")
		if err != nil || got != "flag-tok" {
			t.Errorf("got %q, %v", got, err)
		}
	})
	t.Run("env", func(t *testing.T) {
		t.Setenv("LATERE_ARCA_TOKEN", "env-tok")
		got, err := arcaBearer(t.Context(), "", "")
		if err != nil || got != "env-tok" {
			t.Errorf("got %q, %v", got, err)
		}
	})
	t.Run("not signed in", func(t *testing.T) {
		t.Setenv("LATERE_ARCA_TOKEN", "")
		_, err := arcaBearer(t.Context(), "", "")
		if err == nil || !strings.Contains(err.Error(), "not logged in") || !strings.Contains(err.Error(), "latere login") {
			t.Errorf("want login hint, got %v", err)
		}
	})
}

// The file commands present the same Arca-audience actor token as the git
// helper, never the root token, and name a mint failure for what it is
// rather than as a missing login.
func TestArcaBearerMintsArcaActorToken(t *testing.T) {
	isolateTokens(t)
	t.Setenv("LATERE_ARCA_TOKEN", "")
	writeAuthTokenFile(t, "access-root", "refresh-root", time.Now().Add(time.Hour))

	t.Run("mints", func(t *testing.T) {
		auth := newAuthStub(t)
		got, err := arcaBearer(t.Context(), "", auth.srv.URL)
		if err != nil || got != mintedActor {
			t.Fatalf("arcaBearer = (%q, %v), want the minted Arca token", got, err)
		}
		auth.assertMint(t, "access-root", arcaAudience)
	})
	t.Run("mint failure is reported", func(t *testing.T) {
		auth := newAuthStub(t)
		auth.mintStatus = http.StatusServiceUnavailable
		_, err := arcaBearer(t.Context(), "", auth.srv.URL)
		if err == nil || !strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "not logged in") {
			t.Errorf("arcaBearer error = %v, want the mint failure, not a missing login", err)
		}
	})
}

// Without a saved login Arca refuses with one sentence and sends
// nothing: there is no other credential on disk to fall back to, and a
// bearer minted for one product is never presented to another.
func TestArcaBearerRefusesWithoutALogin(t *testing.T) {
	isolateTokens(t)
	t.Setenv("LATERE_ARCA_TOKEN", "")
	auth := newAuthStub(t)

	got, err := arcaBearer(t.Context(), "", auth.srv.URL)
	if err == nil || !strings.Contains(err.Error(), "not logged in; run `latere login`") {
		t.Errorf("arcaBearer = (%q, %v), want the not-logged-in sentence", got, err)
	}
	if refreshes, mints := auth.counts(); refreshes != 0 || mints != 0 {
		t.Errorf("auth calls = %d refreshes, %d mints; want none without a login", refreshes, mints)
	}
}

// A saved login that cannot be parsed is not "not signed in", but the only
// repair is a new login, so the error must still point there.
func TestArcaBearerUnreadableLoginHintsRelogin(t *testing.T) {
	isolateTokens(t)
	t.Setenv("LATERE_ARCA_TOKEN", "")
	p := filepath.Join(t.TempDir(), "auth-token.json")
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LATERE_AUTH_TOKEN_FILE", p)

	_, err := arcaBearer(t.Context(), "", "")
	if err == nil || !strings.Contains(err.Error(), "parse token file") || !strings.Contains(err.Error(), "latere login") {
		t.Errorf("arcaBearer error = %v, want the parse failure with a re-login hint", err)
	}
}

// execArca runs a arca subcommand against srv with a passthrough token,
// returning stdout and stderr.
func execArca(t *testing.T, srv *httptest.Server, args ...string) (string, string, error) {
	t.Helper()
	cmd := newArcaCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"--api-url", srv.URL, "--token", "test-tok"}, args...))
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

func TestArcaLsListsAndPaginates(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files/me/files" {
			t.Errorf("path = %q", r.URL.Path)
		}
		calls++
		if r.URL.Query().Get("cursor") == "" {
			_ = json.NewEncoder(w).Encode(arca.FileListPage{
				Entries:    []arca.FileEntry{{Path: "files/a.txt", Size: 1}},
				NextCursor: "c2",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(arca.FileListPage{
			Entries: []arca.FileEntry{{Path: "files/b.txt", Size: 2}},
		})
	}))
	defer srv.Close()

	out, _, err := execArca(t, srv, "ls")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected 2 paginated calls, got %d", calls)
	}
	if !strings.Contains(out, "files/a.txt") || !strings.Contains(out, "files/b.txt") {
		t.Errorf("out = %q", out)
	}
}

func TestArcaLsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(arca.FileListPage{Entries: []arca.FileEntry{{Path: "files/a.txt", Size: 7}}})
	}))
	defer srv.Close()

	out, _, err := execArca(t, srv, "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []arca.FileEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if len(entries) != 1 || entries[0].Size != 7 {
		t.Errorf("entries = %+v", entries)
	}
}

func TestArcaGetWritesFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello arca")
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.txt")
	_, stderr, err := execArca(t, srv, "get", "files/a.txt", "-o", dest)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello arca" {
		t.Errorf("file = %q", b)
	}
	if !strings.Contains(stderr, "Downloaded") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestArcaGetStdout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("version") != "2" {
			t.Errorf("version = %q", r.URL.Query().Get("version"))
		}
		fmt.Fprint(w, "v2-bytes")
	}))
	defer srv.Close()

	out, _, err := execArca(t, srv, "get", "files/a.txt", "-o", "-", "--version", "2")
	if err != nil {
		t.Fatal(err)
	}
	if out != "v2-bytes" {
		t.Errorf("stdout = %q", out)
	}
}

func TestArcaPutSmallFile(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		gotBody = b.String()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(arca.FileWriteResult{Path: "files/src.txt", Size: int64(b.Len()), Checksum: "c"})
	}))
	defer srv.Close()

	src := filepath.Join(t.TempDir(), "src.txt")
	if err := os.WriteFile(src, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := execArca(t, srv, "put", src)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/files/me/files/src.txt" {
		t.Errorf("path = %q (default destination should be files/<basename>)", gotPath)
	}
	if gotBody != "small" || !strings.Contains(stderr, "Uploaded") {
		t.Errorf("body=%q stderr=%q", gotBody, stderr)
	}
}

func TestArcaPutStdinRequiresDest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, _, err := execArca(t, srv, "put", "-")
	if err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("want destination error, got %v", err)
	}
}

func TestArcaPutMemoryCASGuidance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionRequired)
		fmt.Fprint(w, `{"error":"memory writes require If-Match or If-None-Match: *"}`)
	}))
	defer srv.Close()

	src := filepath.Join(t.TempDir(), "m.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := execArca(t, srv, "put", src, "memory/m.txt")
	if err == nil || !strings.Contains(err.Error(), "--if-match") || !strings.Contains(err.Error(), "--create-only") {
		t.Fatalf("want CAS guidance, got %v", err)
	}
}

func TestArcaRmVariants(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if _, stderr, err := execArca(t, srv, "rm", "files/a.txt"); err != nil || !strings.Contains(stderr, "Trashed") {
		t.Errorf("rm: %v %q", err, stderr)
	}
	if gotQuery != "" {
		t.Errorf("plain rm sent query %q", gotQuery)
	}
	if _, stderr, err := execArca(t, srv, "rm", "files/a.txt", "--permanent"); err != nil || !strings.Contains(stderr, "Permanently") {
		t.Errorf("rm --permanent: %v %q", err, stderr)
	}
	if gotQuery != "permanent=true" {
		t.Errorf("query = %q", gotQuery)
	}
	if _, stderr, err := execArca(t, srv, "rm", "files/a.txt", "--version", "2"); err != nil || !strings.Contains(stderr, "Pruned version 2") {
		t.Errorf("rm --version: %v %q", err, stderr)
	}
}

// rm --permanent falls back to purging the trash entry when the live file
// is already gone.
func TestArcaRmPermanentPurgesTrashedFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/files/") {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
			return
		}
		if r.URL.Path == "/v1/trash" && r.Method == http.MethodDelete {
			_ = json.NewEncoder(w).Encode(map[string]int{"purged": 1})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, stderr, err := execArca(t, srv, "rm", "files/gone.txt", "--permanent")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Permanently deleted") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestArcaRestoreVariants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/trash/restore":
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["owner"] != "me" || req["path"] != "files/a.txt" {
				t.Errorf("restore req = %v", req)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"path": "files/a.txt", "status": "restored"})
		case "/v1/files/me/files/a.txt":
			var req map[string]int
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["restore_version"] != 3 {
				t.Errorf("restore_version = %v", req)
			}
			_ = json.NewEncoder(w).Encode(arca.VersionRestoreResult{Path: "files/a.txt", RestoredVersion: 3})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	if _, stderr, err := execArca(t, srv, "restore", "files/a.txt"); err != nil || !strings.Contains(stderr, "from trash") {
		t.Errorf("restore: %v %q", err, stderr)
	}
	if _, stderr, err := execArca(t, srv, "restore", "files/a.txt", "--version", "3"); err != nil || !strings.Contains(stderr, "version 3") {
		t.Errorf("restore --version: %v %q", err, stderr)
	}
}

func TestArcaHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["versions"]; !ok {
			t.Error("missing ?versions flag")
		}
		_ = json.NewEncoder(w).Encode(arca.FileVersionListPage{Entries: []arca.FileVersionEntry{
			{VersionNo: 2, Size: 10, Checksum: "c2", CreatedByDisplay: "Changkun", SupersededAt: "2026-07-12T00:00:00Z"},
		}})
	}))
	defer srv.Close()

	out, _, err := execArca(t, srv, "history", "files/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "v2") || !strings.Contains(out, "Changkun") {
		t.Errorf("out = %q", out)
	}
}

func TestArcaShareGranteeInference(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantType string
		wantErr  string
	}{
		{"link", []string{"--link"}, "link", ""},
		{"public", []string{"--public"}, "public", ""},
		{"email", []string{"--to", "a@b.c"}, "email", ""},
		{"principal", []string{"--to", "u-1234"}, "principal", ""},
		{"none", nil, "", "exactly one of"},
		{"conflicting", []string{"--link", "--public"}, "", "exactly one of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got arca.CreateShareRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&got)
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(arca.ShareCreated{ID: "s1", Status: "active", Permission: got.Permission, GranteeType: got.GranteeType, PathPrefix: got.PathPrefix, Owner: "u-test", URL: "/s/tok"})
			}))
			defer srv.Close()

			out, _, err := execArca(t, srv, append([]string{"share", "files/x/"}, tc.args...)...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %q error, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.GranteeType != tc.wantType {
				t.Errorf("grantee_type = %q, want %q", got.GranteeType, tc.wantType)
			}
			if !strings.Contains(out, srv.URL+"/s/tok") {
				t.Errorf("share URL not printed: %q", out)
			}
		})
	}
}

func TestArcaSharesInbox(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(arca.ShareListPage{Entries: []arca.Share{
			{ID: "s1", Status: "active", Permission: "read", GranteeEmail: "a@b.c", PathPrefix: "files/x/"},
		}})
	}))
	defer srv.Close()

	out, _, err := execArca(t, srv, "shares")
	if err != nil || gotPath != "/v1/shares" {
		t.Errorf("shares: %v path=%q", err, gotPath)
	}
	if !strings.Contains(out, "a@b.c") {
		t.Errorf("out = %q", out)
	}
	if _, _, err := execArca(t, srv, "shares", "--inbox"); err != nil || gotPath != "/v1/shared-with-me" {
		t.Errorf("shares --inbox: %v path=%q", err, gotPath)
	}
}

func TestArcaUnshare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/shares/s1" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	_, stderr, err := execArca(t, srv, "unshare", "s1")
	if err != nil || !strings.Contains(stderr, "Revoked") {
		t.Errorf("%v %q", err, stderr)
	}
}

func TestArcaOwnerFlagRoutesToSpace(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(arca.FileListPage{})
	}))
	defer srv.Close()

	if _, _, err := execArca(t, srv, "ls", "--owner", "org"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/files/org/files" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestArcaRejectsNonPositiveVersion(t *testing.T) {
	for _, verb := range []string{"get", "rm", "restore"} {
		for _, version := range []string{"0", "-1"} {
			t.Run(verb+"/"+version, func(t *testing.T) {
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(http.StatusNoContent)
				}))
				defer srv.Close()
				args := []string{verb, "files/a", "--version", version}
				if verb == "get" {
					args = append(args, "-o", "-")
				}
				_, _, err := execArca(t, srv, args...)
				if err == nil || !strings.Contains(err.Error(), "--version") {
					t.Errorf("want version validation error, got %v", err)
				}
				if got := calls.Load(); got != 0 {
					t.Errorf("invalid version made %d requests", got)
				}
			})
		}
	}
}

func TestArcaRmVersionNeverPurgesWholeFile(t *testing.T) {
	var purges atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/trash" {
			purges.Add(1)
			fmt.Fprint(w, `{"purged":1}`)
			return
		}
		if r.URL.Query().Get("version") != "2" {
			t.Errorf("missing version in request: %s", r.URL.String())
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"version not found"}`)
	}))
	defer srv.Close()
	_, stderr, err := execArca(t, srv, "rm", "files/a", "--version", "2", "--permanent")
	if err == nil {
		t.Errorf("missing version reported success: %s", stderr)
	}
	if purges.Load() != 0 {
		t.Fatal("version-scoped deletion purged the entire trash entry")
	}
}

func TestArcaVersionCommandsRequirePath(t *testing.T) {
	for _, verb := range []string{"get", "rm", "restore"} {
		cmd := newArcaCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{verb, "--version", "2"})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "arg") {
			t.Errorf("%s without a path: %v", verb, err)
		}
	}
}

func TestArcaPutEmptyFile(t *testing.T) {
	t.Setenv("LATERE_CELLA_TOKEN", "test-tok")
	src := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(src, nil, 0600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/files/me/files/empty.txt" {
			t.Errorf("unexpected empty upload request: %s %s", r.Method, r.URL.Path)
		}
		if r.ContentLength < 0 {
			w.WriteHeader(http.StatusLengthRequired)
			_, _ = io.WriteString(w, `{"error":"Content-Length is required"}`)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			t.Errorf("empty upload length=%d encoding=%v", r.ContentLength, r.TransferEncoding)
		}
		if body, err := io.ReadAll(r.Body); err != nil || len(body) != 0 {
			t.Errorf("empty upload body=%q, %v", body, err)
		}
		_, _ = io.WriteString(w, `{"path":"files/empty.txt","size":0,"checksum":"empty"}`)
	}))
	defer srv.Close()
	_, stderr, err := execArca(t, srv, "put", src)
	if err != nil {
		t.Fatalf("empty-file upload failed: %v", err)
	}
	if !strings.Contains(stderr, "Uploaded files/empty.txt (0 bytes") {
		t.Fatalf("missing empty-upload success: %q", stderr)
	}
}
