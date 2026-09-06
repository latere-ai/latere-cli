// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"io"
	"mime/multipart"
	"strings"
	"testing"
	"time"
)

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
