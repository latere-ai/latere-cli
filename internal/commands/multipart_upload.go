// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
)

// createUploadPart uses MIME parameter encoding so the server's multipart
// parser recovers control characters instead of treating %0A as a literal name.
func createUploadPart(mw *multipart.Writer, name, filename string) (io.Writer, error) {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name": name, "filename": filename,
	}))
	header.Set("Content-Type", "application/octet-stream")
	return mw.CreatePart(header)
}

type multipartUpload struct {
	*io.PipeReader
	partsDone chan struct{}
	done      chan error
}

// newMultipartUpload streams parts while retaining their completion result.
// HTTP may return a successful response before consuming the request body.
func newMultipartUpload(writeParts func(*multipart.Writer) error) (*multipartUpload, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	upload := &multipartUpload{PipeReader: pr, partsDone: make(chan struct{}), done: make(chan error, 1)}
	go func() {
		err := writeParts(mw)
		close(upload.partsDone)
		if err == nil {
			err = mw.Close()
		}
		_ = pw.CloseWithError(err)
		upload.done <- err
	}()
	return upload, mw.FormDataContentType()
}

// finish is required before reporting success. Once all parts are written,
// closing the reader unblocks any remaining footer write, so waiting is safe.
// Otherwise the producer may be waiting on stdin; reject without waiting on it.
func (u *multipartUpload) finish() error {
	_ = u.Close()
	select {
	case <-u.partsDone:
		if err := <-u.done; err != nil {
			return fmt.Errorf("complete multipart upload: %w", err)
		}
		return nil
	default:
		return errors.New("server responded before multipart upload completed")
	}
}
