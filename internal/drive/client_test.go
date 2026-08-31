package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveURL(t *testing.T) {
	t.Run("flag wins", func(t *testing.T) {
		t.Setenv("DRIVE_API_URL", "https://env.example")
		if got := ResolveURL("https://flag.example/"); got != "https://flag.example" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("env over default", func(t *testing.T) {
		t.Setenv("DRIVE_API_URL", "https://env.example")
		if got := ResolveURL(""); got != "https://env.example" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("default", func(t *testing.T) {
		t.Setenv("DRIVE_API_URL", "")
		if got := ResolveURL(""); got != DefaultBaseURL {
			t.Errorf("got %q", got)
		}
	})
}

func TestFilesPathEscaping(t *testing.T) {
	got := filesPath("me", "files/a dir/b#c.txt")
	want := "/api/v1/files/me/files/a%20dir/b%23c.txt"
	if got != want {
		t.Errorf("filesPath = %q, want %q", got, want)
	}
}

func TestErrorEnvelopeDecoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionRequired)
		fmt.Fprint(w, `{"error":"memory writes require If-Match or If-None-Match: *"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	_, err := c.Put(context.Background(), "me", "memory/x", strings.NewReader("hi"), 2, PutOptions{})
	var derr *Error
	if !asDriveErr(err, &derr) {
		t.Fatalf("want *Error, got %T: %v", err, err)
	}
	if derr.Status != 428 || !strings.Contains(derr.Message, "If-Match") {
		t.Errorf("got %+v", derr)
	}
}

func asDriveErr(err error, out **Error) bool {
	return errors.As(err, out)
}

func TestListPaginatesWithCursor(t *testing.T) {
	var gotCursor, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/me/files" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if _, ok := r.URL.Query()["list"]; !ok {
			t.Error("missing ?list flag")
		}
		gotCursor = r.URL.Query().Get("cursor")
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(FileListPage{
			Entries:    []FileEntry{{Path: "files/a.txt", Size: 3}},
			NextCursor: "next-1",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	page, err := c.List(context.Background(), "me", "files/", "cur-0", 100)
	if err != nil {
		t.Fatal(err)
	}
	if gotCursor != "cur-0" || gotAuth != "Bearer tok" {
		t.Errorf("cursor=%q auth=%q", gotCursor, gotAuth)
	}
	if len(page.Entries) != 1 || page.NextCursor != "next-1" {
		t.Errorf("page = %+v", page)
	}
}

func TestStatParsesHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s", r.Method)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "42")
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Last-Modified", "Sun, 12 Jul 2026 10:00:00 GMT")
	}))
	defer srv.Close()

	info, err := New(srv.URL, "tok").Stat(context.Background(), "me", "files/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 42 || info.Checksum != "abc123" || info.ContentType != "text/plain" || info.Modified.IsZero() {
		t.Errorf("info = %+v", info)
	}
}

func TestDownloadFollowsRedirect(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/api/v1/files/me/files/a.txt", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/presigned/a.txt", http.StatusFound)
	})
	mux.HandleFunc("/presigned/a.txt", func(w http.ResponseWriter, r *http.Request) {
		// Presigned URLs are unauthenticated; the bearer must not leak.
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization forwarded to presigned URL")
		}
		fmt.Fprint(w, "file-bytes")
	})

	body, _, err := New(srv.URL, "tok").Download(context.Background(), "me", "files/a.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	b, _ := io.ReadAll(body)
	if string(b) != "file-bytes" {
		t.Errorf("body = %q", b)
	}
}

func TestPutSetsCASHeaders(t *testing.T) {
	cases := []struct {
		name string
		opts PutOptions
		want map[string]string
	}{
		{"if-match quoted", PutOptions{IfMatch: "abc"}, map[string]string{"If-Match": `"abc"`}},
		{"if-match strips existing quotes", PutOptions{IfMatch: `"abc"`}, map[string]string{"If-Match": `"abc"`}},
		{"create only", PutOptions{CreateOnly: true}, map[string]string{"If-None-Match": "*"}},
		{"content type", PutOptions{ContentType: "text/plain"}, map[string]string{"Content-Type": "text/plain"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got http.Header
			var gotLen int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
				gotLen = r.ContentLength
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(FileWriteResult{Path: "files/a.txt", Size: 2, Checksum: "c"})
			}))
			defer srv.Close()

			res, err := New(srv.URL, "tok").Put(context.Background(), "me", "files/a.txt", strings.NewReader("hi"), 2, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.Checksum != "c" {
				t.Errorf("result = %+v", res)
			}
			if gotLen != 2 {
				t.Errorf("Content-Length = %d, want 2", gotLen)
			}
			for k, v := range tc.want {
				if got.Get(k) != v {
					t.Errorf("%s = %q, want %q", k, got.Get(k), v)
				}
			}
		})
	}
}

func TestDeleteQueryModifiers(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")

	if err := c.Delete(context.Background(), "me", "files/a.txt", true, 0); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "permanent=true" {
		t.Errorf("query = %q", gotQuery)
	}
	if err := c.Delete(context.Background(), "me", "files/a.txt", false, 3); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "version=3" {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestTrashPurgeReturnsCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/trash" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("owner") != "me" || r.URL.Query().Get("path") != "files/a.txt" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"purged": 1})
	}))
	defer srv.Close()

	n, err := New(srv.URL, "tok").TrashPurge(context.Background(), "me", "files/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged = %d", n)
	}
}

// TestMultipartUpload covers the whole multipart flow: session create,
// concurrent part PUTs with exact sizing, ETag collection, and complete.
func TestMultipartUpload(t *testing.T) {
	const partSize = 8 // shrunk for the test; the client honors the server's value
	data := bytes.Repeat([]byte("x"), partSize*2+3)

	var completeBody []byte
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api/v1/uploads", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["owner"] != "me" || req["path"] != "files/big.bin" || req["size"] != float64(len(data)) {
			t.Errorf("create = %v", req)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(uploadSession{
			UploadID: "u1", Owner: "me", Path: "files/big.bin",
			PartSize: partSize, PartCount: 3,
			PartURLs: []string{srv.URL + "/part/1", srv.URL + "/part/2", srv.URL + "/part/3"},
		})
	})
	mux.HandleFunc("/part/", func(w http.ResponseWriter, r *http.Request) {
		n := strings.TrimPrefix(r.URL.Path, "/part/")
		b, _ := io.ReadAll(r.Body)
		wantLen := partSize
		if n == "3" {
			wantLen = 3
		}
		if len(b) != wantLen {
			t.Errorf("part %s: %d bytes, want %d", n, len(b), wantLen)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("bearer leaked to presigned part URL")
		}
		w.Header().Set("ETag", `"etag-`+n+`"`)
	})
	mux.HandleFunc("/api/v1/uploads/u1/complete", func(w http.ResponseWriter, r *http.Request) {
		completeBody, _ = io.ReadAll(r.Body)
		if r.Header.Get("If-None-Match") != "*" {
			t.Error("CAS header missing on complete")
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(FileWriteResult{Path: "files/big.bin", Size: int64(len(data)), Checksum: "composite"})
	})

	res, err := New(srv.URL, "tok").MultipartUpload(context.Background(), "me", "files/big.bin",
		bytes.NewReader(data), int64(len(data)), PutOptions{CreateOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Checksum != "composite" {
		t.Errorf("result = %+v", res)
	}
	var complete struct {
		Parts []struct {
			N    int    `json:"n"`
			Etag string `json:"etag"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(completeBody, &complete); err != nil {
		t.Fatal(err)
	}
	if len(complete.Parts) != 3 {
		t.Fatalf("parts = %+v", complete.Parts)
	}
	for i, p := range complete.Parts {
		if p.N != i+1 || p.Etag != fmt.Sprintf("etag-%d", i+1) {
			t.Errorf("part[%d] = %+v", i, p)
		}
	}
}

// A failing part must abort the session so no orphaned parts hold quota.
func TestMultipartUploadAbortsOnPartFailure(t *testing.T) {
	aborted := make(chan struct{}, 1)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api/v1/uploads", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(uploadSession{
			UploadID: "u2", PartSize: 4, PartCount: 2,
			PartURLs: []string{srv.URL + "/part/1", srv.URL + "/part/2"},
		})
	})
	mux.HandleFunc("/part/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"e1"`)
	})
	mux.HandleFunc("/part/2", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	mux.HandleFunc("/api/v1/uploads/u2", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			aborted <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
		}
	})

	data := []byte("12345678")
	_, err := New(srv.URL, "tok").MultipartUpload(context.Background(), "me", "files/b.bin",
		bytes.NewReader(data), int64(len(data)), PutOptions{})
	if err == nil || !strings.Contains(err.Error(), "part 2/2") {
		t.Fatalf("want part-2 error, got %v", err)
	}
	select {
	case <-aborted:
	default:
		t.Error("session was not aborted after part failure")
	}
}

func TestCreateShareRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req CreateShareRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.GranteeType != "link" || req.Permission != "read" || req.PathPrefix != "files/reports/" {
			t.Errorf("req = %+v", req)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(ShareCreated{ID: "s1", Status: "active", URL: "/s/tok123"})
	}))
	defer srv.Close()

	got, err := New(srv.URL, "tok").CreateShare(context.Background(), CreateShareRequest{
		Owner: "me", PathPrefix: "files/reports/", GranteeType: "link", Permission: "read",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "/s/tok123" {
		t.Errorf("got %+v", got)
	}
}

// TestSimpleEndpointRoundtrips covers the mechanical JSON endpoints in one
// table: method, path, and decoded result for each client call.
func TestSimpleEndpointRoundtrips(t *testing.T) {
	type probe struct{ method, path string }
	var got probe
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = probe{r.Method, r.URL.Path}
		switch {
		case r.URL.Path == "/api/v1/files/me/files/a.txt" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["move_to"]; ok {
				_ = json.NewEncoder(w).Encode(MoveFileResult{Path: "files/b.txt", MovedFrom: "files/a.txt"})
			} else {
				_ = json.NewEncoder(w).Encode(VersionRestoreResult{Path: "files/a.txt", RestoredVersion: 2, Size: 1, Checksum: "c"})
			}
		case r.URL.Path == "/api/v1/files/me/files/a.txt": // ?versions
			_ = json.NewEncoder(w).Encode(FileVersionListPage{Entries: []FileVersionEntry{{VersionNo: 2, Size: 1, Checksum: "c"}}})
		case r.URL.Path == "/api/v1/trash" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(TrashListPage{Entries: []TrashEntry{{Path: "files/t.txt", DeletedAt: "2026-07-12T00:00:00Z"}}})
		case r.URL.Path == "/api/v1/trash/restore":
			_ = json.NewEncoder(w).Encode(map[string]string{"path": "files/t.txt", "status": "restored"})
		case r.URL.Path == "/api/v1/quotas/me":
			_ = json.NewEncoder(w).Encode(QuotaView{Owner: "me", UsedBytes: 10, LimitBytes: 100})
		case r.URL.Path == "/api/v1/shares" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(ShareListPage{Entries: []Share{{ID: "s1", Status: "active"}}})
		case r.URL.Path == "/api/v1/shared-with-me":
			_ = json.NewEncoder(w).Encode(ShareListPage{Entries: []Share{{ID: "s2"}}})
		case r.URL.Path == "/api/v1/shares/s1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	ctx := context.Background()

	if mv, err := c.Move(ctx, "me", "files/a.txt", "files/b.txt"); err != nil || mv.Path != "files/b.txt" {
		t.Errorf("Move: %v %+v", err, mv)
	}
	if rv, err := c.RestoreVersion(ctx, "me", "files/a.txt", 2); err != nil || rv.RestoredVersion != 2 {
		t.Errorf("RestoreVersion: %v %+v", err, rv)
	}
	if vs, err := c.Versions(ctx, "me", "files/a.txt", "", 0); err != nil || len(vs.Entries) != 1 || vs.Entries[0].VersionNo != 2 {
		t.Errorf("Versions: %v %+v", err, vs)
	}
	if tl, err := c.TrashList(ctx, "me", "", 0); err != nil || len(tl.Entries) != 1 {
		t.Errorf("TrashList: %v %+v", err, tl)
	}
	if err := c.TrashRestore(ctx, "me", "files/t.txt"); err != nil {
		t.Errorf("TrashRestore: %v", err)
	}
	if q, err := c.Quota(ctx, "me"); err != nil || q.LimitBytes != 100 {
		t.Errorf("Quota: %v %+v", err, q)
	}
	if sh, err := c.Shares(ctx, false, "", 0); err != nil || sh.Entries[0].ID != "s1" {
		t.Errorf("Shares: %v %+v", err, sh)
	}
	if in, err := c.Shares(ctx, true, "", 0); err != nil || in.Entries[0].ID != "s2" {
		t.Errorf("Shares inbox: %v %+v", err, in)
	}
	if err := c.RevokeShare(ctx, "s1"); err != nil {
		t.Errorf("RevokeShare: %v", err)
	}
	_ = got
}

func TestErrorStringFormats(t *testing.T) {
	if got := (&Error{Status: 404, Message: "not found"}).Error(); got != "drive: not found (HTTP 404)" {
		t.Errorf("got %q", got)
	}
	if got := (&Error{Status: 401}).Error(); got != "drive: HTTP 401" {
		t.Errorf("got %q", got)
	}
}

// Error paths: every endpoint surfaces the {"error": ...} envelope.
func TestEndpointsSurfaceErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"nope"}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	ctx := context.Background()

	calls := map[string]func() error{
		"Download":    func() error { _, _, err := c.Download(ctx, "me", "files/a", 1); return err },
		"Versions":    func() error { _, err := c.Versions(ctx, "me", "files/a", "cur", 5); return err },
		"TrashList":   func() error { _, err := c.TrashList(ctx, "me", "cur", 5); return err },
		"Quota":       func() error { _, err := c.Quota(ctx, "me"); return err },
		"CreateShare": func() error { _, err := c.CreateShare(ctx, CreateShareRequest{}); return err },
		"Shares":      func() error { _, err := c.Shares(ctx, false, "cur", 5); return err },
		"RevokeShare": func() error { return c.RevokeShare(ctx, "s1") },
		"Stat":        func() error { _, err := c.Stat(ctx, "me", "files/a"); return err },
	}
	for name, call := range calls {
		err := call()
		var derr *Error
		if !asDriveErr(err, &derr) || derr.Status != 403 {
			t.Errorf("%s: want 403 *Error, got %v", name, err)
		}
		if name != "Stat" && derr.Message != "nope" {
			t.Errorf("%s: message = %q", name, derr.Message)
		}
	}
}
