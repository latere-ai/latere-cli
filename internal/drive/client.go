// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Package drive is a thin typed client for the Latere Drive API
// (https://drive.latere.ai/api/v1). It mirrors internal/api's conventions
// (Bearer auth, latere-cli User-Agent, typed non-2xx errors) but decodes
// Drive's error envelope, which is a bare {"error": "..."} object.
package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"latere.ai/x/pkg/otel"

	"golang.org/x/sync/errgroup"
)

// DefaultBaseURL is the production Drive deployment.
const DefaultBaseURL = "https://drive.latere.ai"

// PartSize is Drive's fixed multipart part size (16 MiB). Files larger
// than this use the multipart plane; smaller ones stream through a
// single PUT.
const PartSize = 16 << 20

// ResolveURL returns the Drive base URL: flag > DRIVE_API_URL > default.
func ResolveURL(flagURL string) string {
	if flagURL != "" {
		return strings.TrimRight(flagURL, "/")
	}
	if v := os.Getenv("DRIVE_API_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultBaseURL
}

// Client calls the Drive API with a fixed bearer.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP: &http.Client{
			Transport: otel.Transport(nil),
			Timeout:   60 * time.Second,
			// Downloads 302 to presigned object-store URLs. Those carry
			// their auth in the URL and reject requests that also present
			// a bearer, and Go only auto-strips Authorization cross-host —
			// so strip it on every redirect. Reject method changes so a
			// redirected write cannot become a successful-looking GET.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				if previous := via[len(via)-1].Method; req.Method != previous {
					return fmt.Errorf("drive: redirect changed request method from %s to %s", previous, req.Method)
				}
				req.Header.Del("Authorization")
				return nil
			},
		},
	}
}

// Error is a non-2xx Drive API response.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("drive: %s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("drive: HTTP %d", e.Status)
}

// ---- wire types (field names match docs/openapi.yaml in ../drive) ----

type FileEntry struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Checksum    string `json:"checksum,omitempty"`
	IsPublic    bool   `json:"is_public,omitempty"`
	Modified    string `json:"modified,omitempty"`
}

type FileListPage struct {
	Entries    []FileEntry `json:"entries"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

type FileWriteResult struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
	URL      string `json:"url,omitempty"`
}

// Keep an absent byte count distinct from a legitimate empty-file receipt.
type fileWriteReceipt struct {
	FileWriteResult
	Size *int64 `json:"size"`
}

func (r fileWriteReceipt) result(path string, size int64) (*FileWriteResult, error) {
	if r.Path == "" || r.Path != path || r.Size == nil || *r.Size != size {
		return nil, errors.New("drive: upload receipt does not match the requested path and size; upload outcome is unknown")
	}
	r.FileWriteResult.Size = *r.Size
	return &r.FileWriteResult, nil
}

type MoveFileResult struct {
	Path      string `json:"path"`
	MovedFrom string `json:"moved_from,omitempty"`
}

type VersionRestoreResult struct {
	Path            string `json:"path"`
	Size            int64  `json:"size"`
	Checksum        string `json:"checksum"`
	RestoredVersion int    `json:"restored_version"`
}

type FileVersionEntry struct {
	VersionNo        int    `json:"version_no"`
	ContentType      string `json:"content_type"`
	Size             int64  `json:"size"`
	Checksum         string `json:"checksum"`
	CreatedBy        string `json:"created_by"`
	SupersededAt     string `json:"superseded_at"`
	CreatedByDisplay string `json:"created_by_display,omitempty"`
}

type FileVersionListPage struct {
	Entries    []FileVersionEntry `json:"entries"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

type TrashEntry struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	CreatedBy string `json:"created_by"`
	DeletedAt string `json:"deleted_at"`
}

type TrashListPage struct {
	Entries    []TrashEntry `json:"entries"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type QuotaView struct {
	Owner      string `json:"owner"`
	UsedBytes  int64  `json:"used_bytes"`
	LimitBytes int64  `json:"limit_bytes"`
}

type CreateShareRequest struct {
	Owner        string `json:"owner"`
	PathPrefix   string `json:"path_prefix"`
	GranteeType  string `json:"grantee_type"` // principal | email | org | team | role | link | public
	Permission   string `json:"permission"`   // read | write | manage
	GranteeID    string `json:"grantee_id,omitempty"`
	GranteeEmail string `json:"grantee_email,omitempty"`
	GranteeRole  string `json:"grantee_role,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

type ShareCreated struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Permission  string `json:"permission"`
	GranteeType string `json:"grantee_type"`
	PathPrefix  string `json:"path_prefix"`
	Owner       string `json:"owner"`
	URL         string `json:"url,omitempty"`
	Existing    bool   `json:"existing,omitempty"`
}

type Share struct {
	ID               string `json:"id"`
	Owner            string `json:"owner"`
	PathPrefix       string `json:"path_prefix"`
	GranteeType      string `json:"grantee_type"`
	Permission       string `json:"permission"`
	Status           string `json:"status"`
	CreatedBy        string `json:"created_by"`
	CreatedAt        string `json:"created_at"`
	GranteeID        string `json:"grantee_id,omitempty"`
	GranteeEmail     string `json:"grantee_email,omitempty"`
	GranteeRole      string `json:"grantee_role,omitempty"`
	GranteeDisplay   string `json:"grantee_display,omitempty"`
	CreatedByDisplay string `json:"created_by_display,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	Token            string `json:"token,omitempty"`
}

type ShareListPage struct {
	Entries    []Share `json:"entries"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

// FileInfo is the HEAD metadata for one file.
type FileInfo struct {
	ContentType string
	Size        int64
	Checksum    string
	Modified    time.Time
}

// ---- request plumbing ----

// filesPath builds /api/v1/files/{owner}/{path...} with each path segment
// escaped ("/" separators preserved).
func filesPath(owner, p string) string {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/api/v1/files/" + url.PathEscape(owner) + "/" + strings.Join(segs, "/")
}

func (c *Client) req(ctx context.Context, method, path string, query url.Values, body io.Reader) (*http.Request, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("User-Agent", "latere-cli")
	return req, nil
}

// do requires a complete response and one JSON value in out (nil out = drain).
// Non-2xx becomes *Error with Drive's {"error": "..."} message.
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return decodeErr(resp)
	}
	if out == nil {
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(out); err != nil {
		return err
	}
	// The first value can decode before a transfer error is reported.
	// Only whitespace and a clean EOF may follow it.
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("drive: response contains multiple JSON values")
	}
	return nil
}

func decodeErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	var env struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &env)
	if env.Error == "" {
		env.Error = strings.TrimSpace(string(b))
	}
	return &Error{Status: resp.StatusCode, Message: env.Error}
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	req, err := c.req(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := c.req(ctx, http.MethodPost, path, nil, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// ---- files ----

func (c *Client) List(ctx context.Context, owner, prefix, cursor string, limit int) (*FileListPage, error) {
	// Drive rejects a trailing slash on the listing path (it appends its
	// own prefix separator server-side).
	prefix = strings.TrimRight(prefix, "/")
	q := url.Values{"list": {""}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var page FileListPage
	if err := c.getJSON(ctx, filesPath(owner, prefix), q, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func (c *Client) Stat(ctx context.Context, owner, path string) (*FileInfo, error) {
	req, err := c.req(ctx, http.MethodHead, filesPath(owner, path), nil, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		// HEAD carries no body; synthesize the envelope-free error.
		return nil, &Error{Status: resp.StatusCode}
	}
	info := &FileInfo{
		ContentType: resp.Header.Get("Content-Type"),
		Checksum:    strings.Trim(resp.Header.Get("ETag"), `"`),
	}
	info.Size, _ = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	info.Modified, _ = http.ParseTime(resp.Header.Get("Last-Modified"))
	return info, nil
}

// Download returns the file bytes (following Drive's presigned 302) and
// the content length when known (-1 otherwise). The final response must
// be HTTP 200 because this request does not ask for a byte range.
// version 0 = current.
func (c *Client) Download(ctx context.Context, owner, path string, version int) (io.ReadCloser, int64, error) {
	q := url.Values{}
	if version > 0 {
		q.Set("version", strconv.Itoa(version))
	}
	req, err := c.req(ctx, http.MethodGet, filesPath(owner, path), q, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.HTTP.Do(req) // default client policy follows the 302
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode/100 == 2 {
			return nil, 0, fmt.Errorf("drive: expected HTTP 200 for a complete download, got HTTP %d", resp.StatusCode)
		}
		return nil, 0, decodeErr(resp)
	}
	return resp.Body, resp.ContentLength, nil
}

// PutOptions carries the CAS and content-type modifiers for uploads.
type PutOptions struct {
	ContentType string
	IfMatch     string // conditional overwrite against this checksum
	CreateOnly  bool   // If-None-Match: * (fail if the file exists)
}

func (o PutOptions) apply(h http.Header) {
	if o.ContentType != "" {
		h.Set("Content-Type", o.ContentType)
	}
	o.applyCAS(h)
}

// applyCAS sets only the conditional headers — the multipart complete
// call needs CAS without disturbing its JSON Content-Type.
func (o PutOptions) applyCAS(h http.Header) {
	if o.IfMatch != "" {
		h.Set("If-Match", `"`+strings.Trim(o.IfMatch, `"`)+`"`)
	}
	if o.CreateOnly {
		h.Set("If-None-Match", "*")
	}
}

// Put streams a single-request upload. Drive requires Content-Length, so
// size must be known up front.
func (c *Client) Put(ctx context.Context, owner, path string, r io.Reader, size int64, opts PutOptions) (*FileWriteResult, error) {
	req, err := c.req(ctx, http.MethodPut, filesPath(owner, path), nil, r)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	if size == 0 {
		// With a non-nil reader, net/http treats length zero as unknown and
		// sends chunked data. Drive requires an explicit Content-Length: 0.
		if body := req.Body; body != nil {
			defer func() { _ = body.Close() }()
		}
		req.Body = http.NoBody
		req.GetBody = nil
	}
	opts.apply(req.Header)
	var out fileWriteReceipt
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out.result(strings.TrimPrefix(path, "/"), size)
}

func (c *Client) Move(ctx context.Context, owner, path, dest string) (*MoveFileResult, error) {
	var out MoveFileResult
	err := c.postJSON(ctx, filesPath(owner, path), map[string]string{"move_to": dest}, &out)
	if err != nil {
		return nil, err
	}
	if out.Path == "" || out.Path != dest || out.MovedFrom == "" || out.MovedFrom != strings.TrimPrefix(path, "/") {
		return nil, errors.New("drive: move receipt does not match the requested source and destination; move outcome is unknown")
	}
	return &out, nil
}

func (c *Client) RestoreVersion(ctx context.Context, owner, path string, version int) (*VersionRestoreResult, error) {
	var out VersionRestoreResult
	err := c.postJSON(ctx, filesPath(owner, path), map[string]int{"restore_version": version}, &out)
	if err != nil {
		return nil, err
	}
	if out.Path == "" || out.Path != strings.TrimPrefix(path, "/") || out.RestoredVersion <= 0 || out.RestoredVersion != version {
		return nil, errors.New("drive: restore receipt does not match the requested path and version; restore outcome is unknown")
	}
	return &out, nil
}

// Delete trashes a file; permanent hard-deletes it (skipping trash);
// version > 0 prunes that single version instead.
func (c *Client) Delete(ctx context.Context, owner, path string, permanent bool, version int) error {
	q := url.Values{}
	if permanent {
		q.Set("permanent", "true")
	}
	if version > 0 {
		q.Set("version", strconv.Itoa(version))
	}
	req, err := c.req(ctx, http.MethodDelete, filesPath(owner, path), q, nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

func (c *Client) Versions(ctx context.Context, owner, path, cursor string, limit int) (*FileVersionListPage, error) {
	q := url.Values{"versions": {""}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var page FileVersionListPage
	if err := c.getJSON(ctx, filesPath(owner, path), q, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// ---- trash ----

func (c *Client) TrashList(ctx context.Context, owner, cursor string, limit int) (*TrashListPage, error) {
	q := url.Values{"owner": {owner}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var page TrashListPage
	if err := c.getJSON(ctx, "/api/v1/trash", q, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func (c *Client) TrashRestore(ctx context.Context, owner, path string) error {
	var out struct {
		Path   string `json:"path"`
		Status string `json:"status"`
	}
	return c.postJSON(ctx, "/api/v1/trash/restore", map[string]string{"owner": owner, "path": path}, &out)
}

// TrashPurge permanently removes one trashed path, or the whole visible
// trash when path is empty. Returns the purged count.
func (c *Client) TrashPurge(ctx context.Context, owner, path string) (int, error) {
	q := url.Values{"owner": {owner}}
	if path != "" {
		q.Set("path", path)
	}
	req, err := c.req(ctx, http.MethodDelete, "/api/v1/trash", q, nil)
	if err != nil {
		return 0, err
	}
	var out struct {
		Purged int `json:"purged"`
	}
	if err := c.do(req, &out); err != nil {
		return 0, err
	}
	return out.Purged, nil
}

// ---- quota ----

func (c *Client) Quota(ctx context.Context, owner string) (*QuotaView, error) {
	var out QuotaView
	if err := c.getJSON(ctx, "/api/v1/quotas/"+url.PathEscape(owner), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- shares ----

func (c *Client) CreateShare(ctx context.Context, in CreateShareRequest) (*ShareCreated, error) {
	var out ShareCreated
	if err := c.postJSON(ctx, "/api/v1/shares", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Shares(ctx context.Context, inbox bool, cursor string, limit int) (*ShareListPage, error) {
	path := "/api/v1/shares"
	if inbox {
		path = "/api/v1/shared-with-me"
	}
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var page ShareListPage
	if err := c.getJSON(ctx, path, q, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func (c *Client) RevokeShare(ctx context.Context, id string) error {
	req, err := c.req(ctx, http.MethodDelete, "/api/v1/shares/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

// ---- multipart uploads ----

type uploadSession struct {
	UploadID  string   `json:"upload_id"`
	Owner     string   `json:"owner"`
	Path      string   `json:"path"`
	PartSize  int      `json:"part_size"`
	PartCount int      `json:"part_count"`
	PartURLs  []string `json:"part_urls"`
}

// partPutConcurrency bounds in-flight part PUTs, matching the SPA.
const partPutConcurrency = 4

// MultipartUpload uploads a file larger than PartSize through Drive's
// presigned multipart plane: create session, PUT each 16 MiB part to the
// object store (4 in flight), then complete with the collected ETags.
// The session is aborted (best-effort) on any failure so quota is not
// held by orphaned parts.
func (c *Client) MultipartUpload(ctx context.Context, owner, path string, r io.ReaderAt, size int64, opts PutOptions) (*FileWriteResult, error) {
	if size <= 0 {
		return nil, errors.New("drive: upload size must be positive")
	}
	create := map[string]any{"owner": owner, "path": path, "size": size}
	if opts.ContentType != "" {
		create["content_type"] = opts.ContentType
	}
	var sess uploadSession
	if err := c.postJSON(ctx, "/api/v1/uploads", create, &sess); err != nil {
		// A decoded session may precede an incomplete or invalid response
		// tail. Release it even though its response cannot be accepted.
		if sess.UploadID != "" {
			c.abortUpload(ctx, sess.UploadID)
		}
		return nil, err
	}
	// Validate coverage before creating section readers: a missing part would
	// otherwise let completion publish only a prefix of the requested file.
	// Subtract before dividing to avoid overflowing on large declared sizes.
	if sess.UploadID == "" || sess.PartSize <= 0 || sess.PartCount != len(sess.PartURLs) ||
		int64(sess.PartCount) != 1+(size-1)/int64(sess.PartSize) {
		if sess.UploadID != "" {
			c.abortUpload(ctx, sess.UploadID)
		}
		return nil, fmt.Errorf("drive: malformed upload session (part_size=%d, part_count=%d, urls=%d)",
			sess.PartSize, sess.PartCount, len(sess.PartURLs))
	}
	if sess.Path == "" || sess.Path != path {
		c.abortUpload(ctx, sess.UploadID)
		return nil, errors.New("drive: upload session destination does not match the requested path")
	}

	etags := make([]string, sess.PartCount)
	// The group keeps the first part failure and cancels the rest; SetLimit
	// bounds the parts in flight rather than the goroutines spawned.
	g, partCtx := errgroup.WithContext(ctx)
	g.SetLimit(partPutConcurrency)
	for i := range sess.PartCount {
		g.Go(func() error {
			off := int64(i) * int64(sess.PartSize)
			n := min(size-off, int64(sess.PartSize))
			etag, err := putPart(partCtx, c.HTTP, sess.PartURLs[i], io.NewSectionReader(r, off, n), n)
			if err != nil {
				return fmt.Errorf("part %d/%d: %w", i+1, sess.PartCount, err)
			}
			etags[i] = etag
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		c.abortUpload(ctx, sess.UploadID)
		return nil, err
	}

	parts := make([]map[string]any, sess.PartCount)
	for i, etag := range etags {
		parts[i] = map[string]any{"n": i + 1, "etag": etag}
	}
	b, err := json.Marshal(map[string]any{"parts": parts})
	if err != nil {
		c.abortUpload(ctx, sess.UploadID)
		return nil, err
	}
	req, err := c.req(ctx, http.MethodPost, "/api/v1/uploads/"+url.PathEscape(sess.UploadID)+"/complete", nil, bytes.NewReader(b))
	if err != nil {
		c.abortUpload(ctx, sess.UploadID)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	opts.applyCAS(req.Header) // CAS rides the complete call, not the parts
	var out fileWriteReceipt
	if err := c.do(req, &out); err != nil {
		// 412/413 already discard the session server-side; abort is a
		// harmless no-op (404) then.
		c.abortUpload(ctx, sess.UploadID)
		return nil, err
	}
	result, err := out.result(path, size)
	if err != nil {
		c.abortUpload(ctx, sess.UploadID)
		return nil, err
	}
	return result, nil
}

// putPart PUTs one part's bytes to a presigned object-store URL (no
// Authorization header; auth is baked into the URL) and returns the ETag.
func putPart(ctx context.Context, httpc *http.Client, presignedURL string, body io.Reader, size int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, body)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return "", fmt.Errorf("object store HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return "", fmt.Errorf("read object store part response: %w", err)
	}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if etag == "" {
		return "", errors.New("object store returned no ETag for part")
	}
	return etag, nil
}

// abortUpload discards a multipart session. Best-effort cleanup on a
// fresh context: the upload context may already be canceled.
// abortUpload discards a half-finished multipart session server-side.
//
// It runs on WithoutCancel: every caller reaches it on a failure path, and the
// commonest failure is the caller's own context being cancelled. Inheriting
// that context would make the abort a no-op and leave the session, and the
// parts already uploaded, on the server until it expires them.
func (c *Client) abortUpload(ctx context.Context, id string) {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	req, err := c.req(actx, http.MethodDelete, "/api/v1/uploads/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return
	}
	_ = c.do(req, nil)
}
