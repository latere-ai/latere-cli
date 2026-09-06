// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUploadPartPreservesNames(t *testing.T) {
	for _, name := range []string{"plain", "line\nbreak", "carriage\rreturn", "tab\tname", "quote\"back\\slash", "雪 + %0A", "invalid\xff"} {
		t.Run(name, func(t *testing.T) {
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			part, err := createUploadPart(mw, "folder\r\n/"+name, name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(part, "content"); err != nil {
				t.Fatal(err)
			}
			if err := mw.Close(); err != nil {
				t.Fatal(err)
			}
			mr := multipart.NewReader(&body, mw.Boundary())
			got, err := mr.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			if got.FormName() != "folder\r\n/"+name || got.FileName() != filepath.Base(name) {
				t.Errorf("names = (%q, %q), want (%q, %q)", got.FormName(), got.FileName(), "folder\r\n/"+name, filepath.Base(name))
			}
			data, err := io.ReadAll(got)
			if err != nil || string(data) != "content" {
				t.Errorf("content = %q, %v", data, err)
			}
			if _, err := mr.NextPart(); !errors.Is(err, io.EOF) {
				t.Errorf("final boundary = %v, want EOF", err)
			}
		})
	}
}

func TestMultipartUploadCompletion(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "complete"
		var sourceErr error
		if fail {
			name = "source failure"
			sourceErr = errors.New("source read failed")
		}
		t.Run(name, func(t *testing.T) {
			upload, contentType := newMultipartUpload(func(mw *multipart.Writer) error {
				if err := mw.WriteField("file", "contents"); err != nil {
					return err
				}
				return sourceErr
			})
			defer func() { _ = upload.Close() }()
			if !strings.HasPrefix(contentType, "multipart/form-data; boundary=") {
				t.Errorf("invalid multipart content type: %s", contentType)
			}
			if _, err := io.Copy(io.Discard, upload); !errors.Is(err, sourceErr) {
				t.Errorf("body read error = %v, want %v", err, sourceErr)
			}
			if err := upload.finish(); !errors.Is(err, sourceErr) {
				t.Errorf("completion lost source error: %v", err)
			}
		})
	}
}

func TestMultipartUploadRequiresFinalBoundary(t *testing.T) {
	upload, _ := newMultipartUpload(func(mw *multipart.Writer) error { return nil })
	defer func() { _ = upload.Close() }()
	<-upload.partsDone
	// The producer has completed the parts, but nobody consumed the footer.
	if err := upload.finish(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("unsent final boundary returned %v", err)
	}
}

func TestMultipartUploadEarlyResponseDoesNotWaitForInput(t *testing.T) {
	resume := make(chan struct{})
	upload, _ := newMultipartUpload(func(mw *multipart.Writer) error {
		<-resume // Simulate an import blocked reading stdin.
		return nil
	})
	defer func() {
		_ = upload.Close()
		close(resume)
		<-upload.done
	}()
	result := make(chan error, 1)
	go func() { result <- upload.finish() }()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "before multipart upload completed") {
			t.Fatalf("early response returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("early response waited for input")
	}
}
