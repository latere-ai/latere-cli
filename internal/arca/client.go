// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Package arca is a thin typed client for the storage core's /v1 surface,
// which answers at the platform's API origin. It mirrors internal/api's
// conventions (Bearer auth, latere-cli User-Agent, typed non-2xx errors)
// and decodes the error envelope Arca answers with.
//
// The audience the bearer carries is not named here. It is stamped where
// the token is minted, one file per product, so a credential cannot be
// presented to a service it was not addressed to.
package arca

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

// DefaultBaseURL is the platform's API origin, which routes the storage
// prefixes to Arca. It is an origin and not a service host, so it names
// no product and never equals an audience.
const DefaultBaseURL = "https://api.latere.ai"

// PartSize is the fixed multipart part size (16 MiB). Files larger than
// this use the upload session; smaller ones stream through a single PUT.
const PartSize = 16 << 20

// OwnerMe is the one alias a space has. Every other space is named by its
// subject, which a response renders in full so a client can send it back.
const OwnerMe = "me"

// ResolveURL returns the origin to call: flag > ARCA_API_URL > default.
func ResolveURL(flagURL string) string {
	if flagURL != "" {
		return strings.TrimRight(flagURL, "/")
	}
	if v := os.Getenv("ARCA_API_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultBaseURL
}

// Client calls the Arca API with a fixed bearer.
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
					return fmt.Errorf("redirect changed request method from %s to %s", previous, req.Method)
				}
				req.Header.Del("Authorization")
				return nil
			},
		},
	}
}

// Error is a non-2xx response, carrying the family error envelope whole:
// one code, one sentence written for the person, and one developer detail
// beside the request id. The service name is not in the sentence, because
// the person reading it typed the command that made the request.
type Error struct {
	Status    int
	Code      string
	Message   string
	Detail    string
	RequestID string
}

func (e *Error) Error() string {
	out := e.Message
	if out == "" {
		out = fmt.Sprintf("HTTP %d", e.Status)
	}
	if e.Code != "" {
		out = e.Code + ": " + out
	}
	if e.Detail != "" {
		out += "\n" + e.Detail
	}
	if e.RequestID != "" {
		out += "\nrequest " + e.RequestID
	}
	return out
}

// ---- wire types (field names match api/openapi.yaml in ../arca) ----

// Object is one stored object. Every file route that answers JSON answers
// this one shape: a write, a move, a version brought forward, a restore
// from the trash, and every row of a listing.
type Object struct {
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	Checksum     string `json:"checksum"`
	ChecksumKind string `json:"checksum_kind,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	IsPublic     bool   `json:"is_public,omitempty"`
	Modified     string `json:"modified,omitempty"`
	URL          string `json:"url,omitempty"`
}

// Listing is a page of a subtree. Entries are the objects under the path,
// and Prefixes the directory names one level below it, synthesised for a
// tree view; a listing of paths reads Entries alone.
type Listing struct {
	Entries    []Object `json:"entries"`
	Prefixes   []string `json:"prefixes,omitempty"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

// Keep an absent byte count distinct from a legitimate empty-file receipt.
type objectReceipt struct {
	Object
	Size *int64 `json:"size"`
}

func (r objectReceipt) result(path string, size int64) (*Object, error) {
	if r.Path == "" || r.Path != path || r.Size == nil || *r.Size != size {
		return nil, errors.New("the upload receipt does not match the requested path and size; the upload outcome is unknown")
	}
	r.Object.Size = *r.Size
	return &r.Object, nil
}

// Version is one superseded revision of an object.
type Version struct {
	VersionNo    int    `json:"version_no"`
	Size         int64  `json:"size"`
	Checksum     string `json:"checksum"`
	ChecksumKind string `json:"checksum_kind,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	CreatedBy    string `json:"created_by"`
	SupersededAt string `json:"superseded_at"`
}

type VersionPage struct {
	Entries    []Version `json:"entries"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// Trashed is one object in the trash, with the day it stops being
// restorable.
type Trashed struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	CreatedBy string `json:"created_by"`
	DeletedAt string `json:"deleted_at"`
	PurgesAt  string `json:"purges_at,omitempty"`
}

type TrashPage struct {
	Entries    []Trashed `json:"entries"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// The kinds a grant can name. A subject is a person, a service, or an
// organization, all one subject string the authorizer resolves; a link
// and the public are nobody, and the token is the grantee.
const (
	GranteeSubject = "subject"
	GranteeLink    = "link"
	GranteePublic  = "public"
)

// CreateGrantRequest opens a grant to one subject. A link is minted
// through CreateLinkRequest instead: they are two resources.
type CreateGrantRequest struct {
	Owner      string `json:"owner"`
	PathPrefix string `json:"path_prefix"`
	Grantee    string `json:"grantee"`
	Permission string `json:"permission"` // read | write | manage
	ExpiresAt  string `json:"expires_at,omitempty"`
}

// CreateLinkRequest mints a token grant. Kind is link or public, and the
// permission is read: a token grant above read is refused.
type CreateLinkRequest struct {
	Owner      string `json:"owner"`
	PathPrefix string `json:"path_prefix"`
	Kind       string `json:"kind"`
	Permission string `json:"permission,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

// Grant is one share, however it was minted. Grantee carries the subject
// on a subject grant and nothing on a token grant; Token is answered once,
// by the mint that created it, and never by a listing.
type Grant struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	PathPrefix  string `json:"path_prefix"`
	GranteeKind string `json:"grantee_kind"`
	Grantee     string `json:"grantee,omitempty"`
	Permission  string `json:"permission"`
	Status      string `json:"status"`
	Token       string `json:"token,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   string `json:"created_at"`
}

// Link is what a mint answers: the grant, and the path the token is
// redeemed at.
type Link struct {
	Grant
	URL string `json:"url"`
}

type GrantPage struct {
	Entries    []Grant `json:"entries"`
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

// filesPath builds /v1/files/{owner}/{path...} with each path segment
// escaped ("/" separators preserved).
func filesPath(owner, p string) string {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/v1/files/" + url.PathEscape(owner) + "/" + strings.Join(segs, "/")
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

// do requires a complete response and one JSON value in out (nil out =
// drain). Non-2xx becomes *Error with the envelope decoded.
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
		if resp.StatusCode == http.StatusAccepted {
			return errors.New("the request was accepted without confirming completion (HTTP 202); the outcome is unknown")
		}
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
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("the response contains multiple JSON values")
	}
	return nil
}

// decodeErr reads the family envelope, {"error": {"code", "message",
// "details": {"request_id", "detail"}}}. A body that is not that envelope
// is carried as the sentence, so a failure from something other than the
// service is still legible.
func decodeErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				RequestID string `json:"request_id"`
				Detail    string `json:"detail"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &env)
	e := &Error{
		Status:    resp.StatusCode,
		Code:      env.Error.Code,
		Message:   env.Error.Message,
		Detail:    env.Error.Details.Detail,
		RequestID: env.Error.Details.RequestID,
	}
	if e.Code == "" && e.Message == "" {
		e.Message = strings.TrimSpace(string(b))
	}
	return e
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

func (c *Client) List(ctx context.Context, owner, prefix, cursor string, limit int) (*Listing, error) {
	// A prefix is a path, and a path carries no trailing slash; the server
	// appends the separator itself when it reads the subtree.
	prefix = strings.TrimRight(prefix, "/")
	q := url.Values{"list": {""}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var page Listing
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

// Download returns the file bytes (following Arca's presigned 302) and
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
			return nil, 0, fmt.Errorf("a complete download answers HTTP 200, and this answered HTTP %d", resp.StatusCode)
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

// Put streams a single-request upload. Arca requires Content-Length, so
// size must be known up front.
func (c *Client) Put(ctx context.Context, owner, path string, r io.Reader, size int64, opts PutOptions) (*Object, error) {
	req, err := c.req(ctx, http.MethodPut, filesPath(owner, path), nil, r)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	if size == 0 {
		// With a non-nil reader, net/http treats length zero as unknown and
		// sends chunked data. Arca requires an explicit Content-Length: 0.
		if body := req.Body; body != nil {
			defer func() { _ = body.Close() }()
		}
		req.Body = http.NoBody
		req.GetBody = nil
	}
	opts.apply(req.Header)
	var out objectReceipt
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out.result(strings.TrimPrefix(path, "/"), size)
}

// Move renames within a plane. The receipt is the object at its new
// path; the source is not echoed, so the destination is what confirms it.
func (c *Client) Move(ctx context.Context, owner, path, dest string) (*Object, error) {
	var out Object
	err := c.postJSON(ctx, filesPath(owner, path), map[string]string{"move_to": dest}, &out)
	if err != nil {
		return nil, err
	}
	if out.Path == "" || out.Path != dest {
		return nil, errors.New("the move receipt does not name the requested destination; the move outcome is unknown")
	}
	return &out, nil
}

// RestoreVersion brings one revision forward as the current object. The
// receipt is that object; it does not echo which revision was asked for,
// so the path and a checksum are what confirm it.
func (c *Client) RestoreVersion(ctx context.Context, owner, path string, version int) (*Object, error) {
	var out Object
	err := c.postJSON(ctx, filesPath(owner, path), map[string]int{"restore_version": version}, &out)
	if err != nil {
		return nil, err
	}
	if out.Path == "" || out.Path != strings.TrimPrefix(path, "/") || out.Checksum == "" {
		return nil, errors.New("the restore receipt does not match the requested path; the restore outcome is unknown")
	}
	return &out, nil
}

// Delete trashes a file; permanent hard-deletes it (skipping trash);
// version > 0 prunes that single version instead.
func (c *Client) Delete(ctx context.Context, owner, path string, permanent bool, version int) error {
	q := url.Values{}
	if permanent {
		// The flag is read as 0 or 1; any other value is refused.
		q.Set("permanent", "1")
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

func (c *Client) Versions(ctx context.Context, owner, path, cursor string, limit int) (*VersionPage, error) {
	q := url.Values{"versions": {""}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var page VersionPage
	if err := c.getJSON(ctx, filesPath(owner, path), q, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// ---- trash ----

func (c *Client) TrashList(ctx context.Context, owner, cursor string, limit int) (*TrashPage, error) {
	q := url.Values{"owner": {owner}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var page TrashPage
	if err := c.getJSON(ctx, "/v1/trash", q, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// TrashRestore returns one trashed object to its path. The receipt is the
// object now live there, so the path is what confirms the restore.
func (c *Client) TrashRestore(ctx context.Context, owner, path string) error {
	var out Object
	if err := c.postJSON(ctx, "/v1/trash/restore", map[string]string{"owner": owner, "path": path}, &out); err != nil {
		return err
	}
	if out.Path == "" || out.Path != path {
		return errors.New("the trash restore receipt does not name the requested path; the restore outcome is unknown")
	}
	return nil
}

// TrashPurge permanently removes one trashed path, or the whole visible
// trash when path is empty. Returns the purged count.
func (c *Client) TrashPurge(ctx context.Context, owner, path string) (int, error) {
	q := url.Values{"owner": {owner}}
	if path != "" {
		q.Set("path", path)
	}
	req, err := c.req(ctx, http.MethodDelete, "/v1/trash", q, nil)
	if err != nil {
		return 0, err
	}
	var out struct {
		Purged *int `json:"purged"`
	}
	if err := c.do(req, &out); err != nil {
		return 0, err
	}
	if out.Purged == nil || *out.Purged < 0 {
		return 0, errors.New("the trash purge receipt carries no nonnegative purged count; the purge outcome is unknown")
	}
	return *out.Purged, nil
}

// ---- shares ----

// CreateGrant grants one subject a permission on a subtree.
func (c *Client) CreateGrant(ctx context.Context, in CreateGrantRequest) (*Grant, error) {
	var out Grant
	if err := c.postJSON(ctx, "/v1/shares", in, &out); err != nil {
		return nil, err
	}
	if err := validateGrantReceipt(in, out); err != nil {
		return nil, fmt.Errorf("the share creation receipt is invalid (share %q): %w; the creation outcome is unknown", out.ID, err)
	}
	return &out, nil
}

func validateGrantReceipt(in CreateGrantRequest, out Grant) error {
	if err := validateShareCommon(out, in.Owner, in.PathPrefix, in.Permission); err != nil {
		return err
	}
	switch {
	case out.GranteeKind != GranteeSubject:
		return fmt.Errorf("grantee kind is %q, and a subject grant answers %q", out.GranteeKind, GranteeSubject)
	case out.Grantee == "" || out.Grantee != in.Grantee:
		return errors.New("grantee does not match the request")
	}
	return nil
}

// CreateLink mints a token grant, whose token is answered once.
func (c *Client) CreateLink(ctx context.Context, in CreateLinkRequest) (*Link, error) {
	var out Link
	if err := c.postJSON(ctx, "/v1/shares/links", in, &out); err != nil {
		return nil, err
	}
	if err := validateLinkReceipt(in, out); err != nil {
		return nil, fmt.Errorf("the share creation receipt is invalid (share %q): %w; the creation outcome is unknown", out.ID, err)
	}
	return &out, nil
}

func validateLinkReceipt(in CreateLinkRequest, out Link) error {
	// A token grant is read whatever the request left unsaid, so the
	// permission is checked against the rule rather than against the ask.
	if err := validateShareCommon(out.Grant, in.Owner, in.PathPrefix, "read"); err != nil {
		return err
	}
	switch {
	case out.GranteeKind != in.Kind:
		return errors.New("grantee kind does not match the request")
	case strings.TrimSpace(out.Token) == "":
		return errors.New("the token grant carries no token, which is answered once and never again")
	case strings.TrimSpace(out.URL) == "":
		return errors.New("the token grant carries no address to redeem it at")
	}
	return nil
}

func validateShareCommon(out Grant, owner, prefix, permission string) error {
	switch {
	case strings.TrimSpace(out.ID) == "":
		return errors.New("missing share ID")
	case out.Status != "active":
		return fmt.Errorf("share status is %q, and a share is created active", out.Status)
	case out.Permission == "" || out.Permission != permission:
		return errors.New("permission does not match the request")
	case out.PathPrefix == "" || out.PathPrefix != prefix:
		return errors.New("path prefix does not match the request")
	case strings.TrimSpace(out.Owner) == "":
		return errors.New("missing owner")
	// The server resolves the one alias and renders the subject in full;
	// an explicit subject must come back unchanged.
	case owner != OwnerMe && out.Owner != owner:
		return errors.New("owner does not match the request")
	}
	return nil
}

// Grants lists the subject grants on a space.
func (c *Client) Grants(ctx context.Context, owner, cursor string, limit int) (*GrantPage, error) {
	return c.grantPage(ctx, "/v1/shares", owner, cursor, limit)
}

// Links lists the token grants on a space. They are a second resource,
// so a whole picture of who can reach a space reads both.
func (c *Client) Links(ctx context.Context, owner, cursor string, limit int) (*GrantPage, error) {
	return c.grantPage(ctx, "/v1/shares/links", owner, cursor, limit)
}

// SharedWithMe lists the grants whose grantee is the caller. It takes no
// owner: the caller is the grantee, not the space.
func (c *Client) SharedWithMe(ctx context.Context, cursor string, limit int) (*GrantPage, error) {
	return c.grantPage(ctx, "/v1/shares/with-me", "", cursor, limit)
}

func (c *Client) grantPage(ctx context.Context, path, owner, cursor string, limit int) (*GrantPage, error) {
	q := url.Values{}
	if owner != "" {
		q.Set("owner", owner)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var page GrantPage
	if err := c.getJSON(ctx, path, q, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// RevokeShare withdraws one grant. The two kinds live in two id spaces
// and a response says nothing about which an id belongs to, so a subject
// grant is tried first and a token grant on its 404.
func (c *Client) RevokeShare(ctx context.Context, id string) error {
	err := c.revoke(ctx, "/v1/shares/"+url.PathEscape(id))
	var derr *Error
	if errors.As(err, &derr) && derr.Status == http.StatusNotFound {
		return c.revoke(ctx, "/v1/shares/links/"+url.PathEscape(id))
	}
	return err
}

func (c *Client) revoke(ctx context.Context, path string) error {
	req, err := c.req(ctx, http.MethodDelete, path, nil, nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

// ---- multipart uploads ----

type uploadSession struct {
	ID        string   `json:"id"`
	Owner     string   `json:"owner"`
	Path      string   `json:"path"`
	PartSize  int64    `json:"part_size"`
	PartCount int64    `json:"part_count"`
	PartURLs  []string `json:"part_urls"`
	ExpiresAt string   `json:"expires_at"`
}

// partPutConcurrency bounds in-flight part PUTs, matching the SPA.
const partPutConcurrency = 4

// MultipartUpload uploads a file larger than PartSize through the upload
// session: open the session, PUT each 16 MiB part to the object store (4
// in flight), then complete with the collected ETags. The session is
// aborted (best-effort) on any failure, so no bytes are left counted
// against the space by parts nothing will assemble.
func (c *Client) MultipartUpload(ctx context.Context, owner, path string, r io.ReaderAt, size int64, opts PutOptions) (*Object, error) {
	if size <= 0 {
		return nil, errors.New("an upload size is a positive number of bytes")
	}
	create := map[string]any{"owner": owner, "path": path, "size": size}
	if opts.ContentType != "" {
		create["content_type"] = opts.ContentType
	}
	var sess uploadSession
	if err := c.postJSON(ctx, "/v1/uploads", create, &sess); err != nil {
		// A decoded session may precede an incomplete or invalid response
		// tail. Release it even though its response cannot be accepted.
		if sess.ID != "" {
			c.abortUpload(ctx, sess.ID)
		}
		return nil, err
	}
	// Validate coverage before creating section readers: a missing part would
	// otherwise let completion publish only a prefix of the requested file.
	// Subtract before dividing to avoid overflowing on large declared sizes.
	if sess.ID == "" || sess.PartSize <= 0 || sess.PartCount != int64(len(sess.PartURLs)) ||
		sess.PartCount != 1+(size-1)/sess.PartSize {
		if sess.ID != "" {
			c.abortUpload(ctx, sess.ID)
		}
		return nil, fmt.Errorf("the upload session is malformed (part_size=%d, part_count=%d, urls=%d)",
			sess.PartSize, sess.PartCount, len(sess.PartURLs))
	}
	if sess.Path == "" || sess.Path != path {
		c.abortUpload(ctx, sess.ID)
		return nil, errors.New("the upload session names a destination other than the requested path")
	}

	// The count equals the number of URLs the response carried, so it is
	// bounded by the response itself and fits an index.
	count := int(sess.PartCount)
	etags := make([]string, count)
	// The group keeps the first part failure and cancels the rest; SetLimit
	// bounds the parts in flight rather than the goroutines spawned.
	g, partCtx := errgroup.WithContext(ctx)
	g.SetLimit(partPutConcurrency)
	for i := range count {
		g.Go(func() error {
			off := int64(i) * sess.PartSize
			n := min(size-off, sess.PartSize)
			etag, err := putPart(partCtx, c.HTTP, sess.PartURLs[i], io.NewSectionReader(r, off, n), n)
			if err != nil {
				return fmt.Errorf("part %d/%d: %w", i+1, count, err)
			}
			etags[i] = etag
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		c.abortUpload(ctx, sess.ID)
		return nil, err
	}

	parts := make([]map[string]any, count)
	for i, etag := range etags {
		parts[i] = map[string]any{"n": i + 1, "etag": etag}
	}
	b, err := json.Marshal(map[string]any{"parts": parts})
	if err != nil {
		c.abortUpload(ctx, sess.ID)
		return nil, err
	}
	req, err := c.req(ctx, http.MethodPost, "/v1/uploads/"+url.PathEscape(sess.ID)+"/complete", nil, bytes.NewReader(b))
	if err != nil {
		c.abortUpload(ctx, sess.ID)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	opts.applyCAS(req.Header) // CAS rides the complete call, not the parts
	var out objectReceipt
	if err := c.do(req, &out); err != nil {
		// 412/413 already discard the session server-side; abort is a
		// harmless no-op (404) then.
		c.abortUpload(ctx, sess.ID)
		return nil, err
	}
	result, err := out.result(path, size)
	if err != nil {
		c.abortUpload(ctx, sess.ID)
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
	req, err := c.req(actx, http.MethodDelete, "/v1/uploads/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return
	}
	_ = c.do(req, nil)
}
