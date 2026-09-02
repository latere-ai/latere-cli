// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestCeFileCommandUseStrings(t *testing.T) {
	cases := []struct {
		cmd  *cobra.Command
		want string
	}{
		{newCeCatCmd(), "cat <name|id> <path>"},
		{newCeWriteCmd(), "write <name|id> <path>"},
		{newCeLsCmd(), "ls <name|id> <path>"},
		{newCeUploadCmd(), "upload <name|id> <src...> --dest D"},
		{newCeMkdirCmd(), "mkdir <name|id> <path>"},
		{newCeRmCmd(), "rm <name|id> <path>"},
		{newCeMvCmd(), "mv <name|id> <from> <to>"},
	}
	for _, c := range cases {
		if c.cmd.Use != c.want {
			t.Errorf("Use = %q, want %q", c.cmd.Use, c.want)
		}
	}
}

// write composes a PUT with a base64 body, the same shape the command sends.
func TestCeWriteComposition(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &api.Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	body, _ := json.Marshal(map[string]any{
		"path":    "/workspace/note.txt",
		"content": base64.StdEncoding.EncodeToString([]byte("hi")),
	})
	if err := c.Do(t.Context(), http.MethodPut, sbPath("dev")+"/files",
		bytes.NewReader(body), "application/json", nil); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/v1/sandboxes/dev/files" {
		t.Fatalf("method=%s path=%s", gotMethod, gotPath)
	}
	var sent struct{ Path, Content string }
	_ = json.Unmarshal(gotBody, &sent)
	if sent.Path != "/workspace/note.txt" {
		t.Fatalf("path = %q", sent.Path)
	}
	dec, _ := base64.StdEncoding.DecodeString(sent.Content)
	if string(dec) != "hi" {
		t.Fatalf("content = %q", dec)
	}
}

// rm issues a DELETE to the files path with the target as a query param.
func TestCeRmComposition(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &api.Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	path := sbPath("dev") + "/files?path=" + "%2Fworkspace%2Fold"
	if err := c.Do(t.Context(), http.MethodDelete, path, nil, "", nil); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v1/sandboxes/dev/files" {
		t.Fatalf("method=%s path=%s", gotMethod, gotPath)
	}
	if !strings.Contains(gotQuery, "path=%2Fworkspace%2Fold") {
		t.Fatalf("query=%s", gotQuery)
	}
}

// upload sends each file as a multipart part whose form-field name is the
// destination-relative path, so folders survive.
func TestCeUploadFieldNameCarriesPath(t *testing.T) {
	var gotDest, gotFieldName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if part.FileName() == "" && part.FormName() == "dest" {
				b, _ := io.ReadAll(part)
				gotDest = string(b)
				continue
			}
			gotFieldName = part.FormName()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dest":"/workspace","files":1,"bytes":3}`))
	}))
	defer srv.Close()

	// Compose the same body the upload command builds.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	ct := mw.FormDataContentType()
	go func() {
		_ = mw.WriteField("dest", "/workspace")
		part, _ := mw.CreateFormFile("dist/app.js", "app.js")
		_, _ = part.Write([]byte("abc"))
		_ = pw.CloseWithError(mw.Close())
	}()

	c := &api.Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	var resp struct {
		Files int `json:"files"`
	}
	if err := c.Do(t.Context(), http.MethodPost, sbPath("dev")+"/files/upload", pr, ct, &resp); err != nil {
		t.Fatal(err)
	}
	if gotDest != "/workspace" {
		t.Fatalf("dest = %q", gotDest)
	}
	if gotFieldName != "dist/app.js" {
		t.Fatalf("field name = %q, want dist/app.js (folder path preserved)", gotFieldName)
	}
	// Sanity that the content type carried a multipart boundary.
	if mt, _, _ := mime.ParseMediaType(ct); mt != "multipart/form-data" {
		t.Fatalf("content type = %q", ct)
	}
}

// ls composes a list GET and decodes the entries envelope.
func TestCeLsComposition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "list=true") {
			http.Error(w, "missing list=true", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":[{"name":"a.txt","size":3,"mode":420,"is_directory":false}]}`))
	}))
	defer srv.Close()

	c := &api.Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	var resp struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	path := sbPath("dev") + "/files?path=" + "%2Fworkspace" + "&list=true"
	if err := c.Do(t.Context(), http.MethodGet, path, nil, "", &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Name != "a.txt" {
		t.Fatalf("entries = %+v", resp.Entries)
	}
}
