package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latere-ai/latere-cli/internal/drive"
)

func TestDriveCommandRegisteredInRoot(t *testing.T) {
	for _, c := range NewRoot("test").Commands() {
		if c.Name() == "drive" {
			return
		}
	}
	t.Fatal("'drive' command not registered in root")
}

func TestDriveVerbSet(t *testing.T) {
	// The command space is deliberately small (specs/drive-subcommand.md);
	// growing it should be a conscious spec change, not a drive-by.
	want := map[string]bool{
		"ls": false, "get": false, "put": false, "mv": false, "rm": false,
		"restore": false, "history": false, "share": false, "shares": false, "unshare": false,
	}
	for _, c := range newDriveCmd().Commands() {
		name := c.Name()
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected drive verb %q — update the spec first", name)
			continue
		}
		want[name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing drive verb %q", name)
		}
	}
}

func TestSkipUpdateCheckForDrive(t *testing.T) {
	root := NewRoot("test")
	cmd, _, err := root.Find([]string{"drive", "get"})
	if err != nil {
		t.Fatal(err)
	}
	if !skipUpdateCheck(cmd) {
		t.Error("drive subcommands must skip the update check (get -o - streams to stdout)")
	}
}

func TestDriveBearerPrecedence(t *testing.T) {
	t.Setenv("LATERE_AUTH_TOKEN_FILE", "/nonexistent/auth.json")
	t.Setenv("LATERE_TOKEN_FILE", "/nonexistent/token.json")

	t.Run("flag wins", func(t *testing.T) {
		t.Setenv("LATERE_DRIVE_TOKEN", "env-tok")
		got, err := driveBearer(t.Context(), "flag-tok", "")
		if err != nil || got != "flag-tok" {
			t.Errorf("got %q, %v", got, err)
		}
	})
	t.Run("env", func(t *testing.T) {
		t.Setenv("LATERE_DRIVE_TOKEN", "env-tok")
		got, err := driveBearer(t.Context(), "", "")
		if err != nil || got != "env-tok" {
			t.Errorf("got %q, %v", got, err)
		}
	})
	t.Run("not signed in", func(t *testing.T) {
		t.Setenv("LATERE_DRIVE_TOKEN", "")
		_, err := driveBearer(t.Context(), "", "")
		if err == nil || !strings.Contains(err.Error(), "latere login") {
			t.Errorf("want login hint, got %v", err)
		}
	})
}

// execDrive runs a drive subcommand against srv with a passthrough token,
// returning stdout and stderr.
func execDrive(t *testing.T, srv *httptest.Server, args ...string) (string, string, error) {
	t.Helper()
	cmd := newDriveCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"--drive-url", srv.URL, "--token", "test-tok"}, args...))
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

func TestDriveLsListsAndPaginates(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/me/files" {
			t.Errorf("path = %q", r.URL.Path)
		}
		calls++
		if r.URL.Query().Get("cursor") == "" {
			_ = json.NewEncoder(w).Encode(drive.FileListPage{
				Entries:    []drive.FileEntry{{Path: "files/a.txt", Size: 1}},
				NextCursor: "c2",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(drive.FileListPage{
			Entries: []drive.FileEntry{{Path: "files/b.txt", Size: 2}},
		})
	}))
	defer srv.Close()

	out, _, err := execDrive(t, srv, "ls")
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

func TestDriveLsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(drive.FileListPage{Entries: []drive.FileEntry{{Path: "files/a.txt", Size: 7}}})
	}))
	defer srv.Close()

	out, _, err := execDrive(t, srv, "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []drive.FileEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if len(entries) != 1 || entries[0].Size != 7 {
		t.Errorf("entries = %+v", entries)
	}
}

func TestDriveGetWritesFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello drive")
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.txt")
	_, stderr, err := execDrive(t, srv, "get", "files/a.txt", "-o", dest)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello drive" {
		t.Errorf("file = %q", b)
	}
	if !strings.Contains(stderr, "Downloaded") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestDriveGetStdout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("version") != "2" {
			t.Errorf("version = %q", r.URL.Query().Get("version"))
		}
		fmt.Fprint(w, "v2-bytes")
	}))
	defer srv.Close()

	out, _, err := execDrive(t, srv, "get", "files/a.txt", "-o", "-", "--version", "2")
	if err != nil {
		t.Fatal(err)
	}
	if out != "v2-bytes" {
		t.Errorf("stdout = %q", out)
	}
}

func TestDrivePutSmallFile(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		gotBody = b.String()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(drive.FileWriteResult{Path: "files/src.txt", Size: int64(b.Len()), Checksum: "c"})
	}))
	defer srv.Close()

	src := filepath.Join(t.TempDir(), "src.txt")
	if err := os.WriteFile(src, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := execDrive(t, srv, "put", src)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/files/me/files/src.txt" {
		t.Errorf("path = %q (default destination should be files/<basename>)", gotPath)
	}
	if gotBody != "small" || !strings.Contains(stderr, "Uploaded") {
		t.Errorf("body=%q stderr=%q", gotBody, stderr)
	}
}

func TestDrivePutStdinRequiresDest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, _, err := execDrive(t, srv, "put", "-")
	if err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("want destination error, got %v", err)
	}
}

func TestDrivePutMemoryCASGuidance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionRequired)
		fmt.Fprint(w, `{"error":"memory writes require If-Match or If-None-Match: *"}`)
	}))
	defer srv.Close()

	src := filepath.Join(t.TempDir(), "m.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := execDrive(t, srv, "put", src, "memory/m.txt")
	if err == nil || !strings.Contains(err.Error(), "--if-match") || !strings.Contains(err.Error(), "--create-only") {
		t.Fatalf("want CAS guidance, got %v", err)
	}
}

func TestDriveRmVariants(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if _, stderr, err := execDrive(t, srv, "rm", "files/a.txt"); err != nil || !strings.Contains(stderr, "Trashed") {
		t.Errorf("rm: %v %q", err, stderr)
	}
	if gotQuery != "" {
		t.Errorf("plain rm sent query %q", gotQuery)
	}
	if _, stderr, err := execDrive(t, srv, "rm", "files/a.txt", "--permanent"); err != nil || !strings.Contains(stderr, "Permanently") {
		t.Errorf("rm --permanent: %v %q", err, stderr)
	}
	if gotQuery != "permanent=true" {
		t.Errorf("query = %q", gotQuery)
	}
	if _, stderr, err := execDrive(t, srv, "rm", "files/a.txt", "--version", "2"); err != nil || !strings.Contains(stderr, "Pruned version 2") {
		t.Errorf("rm --version: %v %q", err, stderr)
	}
}

// rm --permanent falls back to purging the trash entry when the live file
// is already gone.
func TestDriveRmPermanentPurgesTrashedFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/files/") {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
			return
		}
		if r.URL.Path == "/api/v1/trash" && r.Method == http.MethodDelete {
			_ = json.NewEncoder(w).Encode(map[string]int{"purged": 1})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, stderr, err := execDrive(t, srv, "rm", "files/gone.txt", "--permanent")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Permanently deleted") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestDriveRestoreVariants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/trash/restore":
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["owner"] != "me" || req["path"] != "files/a.txt" {
				t.Errorf("restore req = %v", req)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"path": "files/a.txt", "status": "restored"})
		case "/api/v1/files/me/files/a.txt":
			var req map[string]int
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["restore_version"] != 3 {
				t.Errorf("restore_version = %v", req)
			}
			_ = json.NewEncoder(w).Encode(drive.VersionRestoreResult{Path: "files/a.txt", RestoredVersion: 3})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	if _, stderr, err := execDrive(t, srv, "restore", "files/a.txt"); err != nil || !strings.Contains(stderr, "from trash") {
		t.Errorf("restore: %v %q", err, stderr)
	}
	if _, stderr, err := execDrive(t, srv, "restore", "files/a.txt", "--version", "3"); err != nil || !strings.Contains(stderr, "version 3") {
		t.Errorf("restore --version: %v %q", err, stderr)
	}
}

func TestDriveHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["versions"]; !ok {
			t.Error("missing ?versions flag")
		}
		_ = json.NewEncoder(w).Encode(drive.FileVersionListPage{Entries: []drive.FileVersionEntry{
			{VersionNo: 2, Size: 10, Checksum: "c2", CreatedByDisplay: "Changkun", SupersededAt: "2026-07-12T00:00:00Z"},
		}})
	}))
	defer srv.Close()

	out, _, err := execDrive(t, srv, "history", "files/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "v2") || !strings.Contains(out, "Changkun") {
		t.Errorf("out = %q", out)
	}
}

func TestDriveShareGranteeInference(t *testing.T) {
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
			var got drive.CreateShareRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&got)
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(drive.ShareCreated{ID: "s1", Status: "active", URL: "/s/tok"})
			}))
			defer srv.Close()

			out, _, err := execDrive(t, srv, append([]string{"share", "files/x/"}, tc.args...)...)
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

func TestDriveSharesInbox(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(drive.ShareListPage{Entries: []drive.Share{
			{ID: "s1", Status: "active", Permission: "read", GranteeEmail: "a@b.c", PathPrefix: "files/x/"},
		}})
	}))
	defer srv.Close()

	out, _, err := execDrive(t, srv, "shares")
	if err != nil || gotPath != "/api/v1/shares" {
		t.Errorf("shares: %v path=%q", err, gotPath)
	}
	if !strings.Contains(out, "a@b.c") {
		t.Errorf("out = %q", out)
	}
	if _, _, err := execDrive(t, srv, "shares", "--inbox"); err != nil || gotPath != "/api/v1/shared-with-me" {
		t.Errorf("shares --inbox: %v path=%q", err, gotPath)
	}
}

func TestDriveUnshare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/shares/s1" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	_, stderr, err := execDrive(t, srv, "unshare", "s1")
	if err != nil || !strings.Contains(stderr, "Revoked") {
		t.Errorf("%v %q", err, stderr)
	}
}

func TestDriveOwnerFlagRoutesToSpace(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(drive.FileListPage{})
	}))
	defer srv.Close()

	if _, _, err := execDrive(t, srv, "ls", "--owner", "org"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/files/org/files" {
		t.Errorf("path = %q", gotPath)
	}
}
