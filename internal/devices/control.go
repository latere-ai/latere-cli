// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package devices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"latere.ai/x/pkg/httpjson"
)

// The control socket: HTTP with JSON over a Unix socket in the engine's
// state directory, versioned under /v1, so a desktop application and the
// CLI it bundles can differ by a release. Only a process of the engine's
// own user is accepted, by the peer's credentials, and the state directory
// is on every session's deny list, so no session's command reaches it.

// maxSocketPath is the longest path a Unix socket address holds on macOS,
// whose sun_path is 104 bytes with its terminator; Linux allows 108.
const maxSocketPath = 103

// controlClient is how an answer given over the socket names its client
// when the request names none.
const controlClient = "another client"

// controlListener accepts a connection only from a process of uid.
type controlListener struct {
	*net.UnixListener
	uid  int
	peer func(*net.UnixConn) (int, error)
}

// Accept answers the next connection from the engine's own user, closing
// every other one unread.
func (l *controlListener) Accept() (net.Conn, error) {
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		if uid, err := l.peer(c); err == nil && uid == l.uid {
			return c, nil
		}
		_ = c.Close()
	}
}

// ListenControl opens the control socket at path, mode 0600 in a directory
// of mode 0700. A socket left by an engine that ended is replaced: the
// caller holds the lock, so no live engine owns it.
func ListenControl(path string) (net.Listener, error) {
	return listenControl(path, os.Getuid(), peerUID)
}

func listenControl(path string, uid int, peer func(*net.UnixConn) (int, error)) (net.Listener, error) {
	if len(path) > maxSocketPath {
		return nil, Fail(CodeControlUnavailable, fmt.Sprintf(
			"the socket path %s is %d bytes, longer than the %d a socket address holds; set XDG_STATE_HOME to a shorter directory",
			path, len(path), maxSocketPath), nil)
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return nil, Fail(CodeControlUnavailable, "", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, Fail(CodeControlUnavailable, "", err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, Fail(CodeControlUnavailable, "", err)
	}
	ln.SetUnlinkOnClose(true)
	if err = os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, Fail(CodeControlUnavailable, "", err)
	}
	return &controlListener{UnixListener: ln, uid: uid, peer: peer}, nil
}

// ServeControl answers the local API on ln until ctx ends.
func (e *Engine) ServeControl(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           e.ControlHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		// A follower's stream lasts as long as its client, so the server is
		// closed rather than drained.
		_ = srv.Close()
	}()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		<-stopped
		return nil
	}
	return err
}

// ControlHandler is the local API.
func (e *Engine) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.Write(w, http.StatusOK, e.Status())
	})
	mux.HandleFunc("GET /v1/activity", e.handleActivity)
	mux.HandleFunc("GET /v1/approvals", e.handleApprovals)
	mux.HandleFunc("POST /v1/approvals/{op}", e.handleAnswer)
	for route, act := range map[string]func() error{
		"POST /v1/pause": e.Pause, "POST /v1/resume": e.Resume, "POST /v1/reload": e.Reload,
	} {
		mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) {
			if err := act(); err != nil {
				writeControlError(w, err)
				return
			}
			httpjson.Write(w, http.StatusOK, e.Status())
		})
	}
	return mux
}

// writeControlError answers a refusal of the local API in the family's
// envelope: one code, one fixed sentence, the detail beside it.
func writeControlError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, CodeServeFailed, refusals[CodeServeFailed].message
	var de *Error
	switch {
	case errors.Is(err, ErrNotRunning):
		status, code, message = http.StatusConflict, "not_serving", "This computer is not serving right now."
	case errors.Is(err, ErrNotPending):
		status, code, message = http.StatusNotFound, "not_pending", "Nothing waits for an answer under this id; it was answered or withdrawn."
	case errors.Is(err, ErrNotRememberable):
		status, code, message = http.StatusBadRequest, "not_rememberable", "Only a trigger that fires on a schedule can be remembered."
	case errors.Is(err, ErrBadAnswer):
		status, code, message = http.StatusBadRequest, "invalid_answer", "Answer allow, deny or remember."
	case errors.As(err, &de):
		status, code, message = http.StatusUnprocessableEntity, de.Code, de.Message
	}
	httpjson.WriteError(w, status, httpjson.Error{Code: code, Message: message, Details: map[string]any{"detail": err.Error()}})
}

// handleActivity answers the activity log, from since when given, and with
// follow=1 keeps the stream open with each line as it is written.
func (e *Engine) handleActivity(w http.ResponseWriter, r *http.Request) {
	since, err := ParseSince(r.URL.Query().Get("since"), e.cfg.Now())
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, httpjson.Error{
			Code: "invalid_since", Message: "Give since as a time such as 2026-10-18T09:00:00Z or a duration such as 1h.",
			Details: map[string]any{"detail": err.Error()},
		})
		return
	}
	e.mu.Lock()
	env := e.policy.Device
	e.mu.Unlock()
	follow := r.URL.Query().Get("follow") == "1"
	var events <-chan Event
	if follow {
		var stop func()
		events, stop = e.Subscribe()
		defer stop()
	}
	entries, err := ReadActivity(e.cfg.Paths.ActivityDir(env), since)
	if err != nil {
		writeControlError(w, Fail(CodeServeFailed, "read the activity log", err))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	stream := newStream(w)
	for _, entry := range entries {
		if !stream.send(entry) {
			return
		}
	}
	if !follow {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Kind == EventActivity && !stream.send(ev.Activity) {
				return
			}
		}
	}
}

// handleApprovals answers the creates waiting for the person, and with
// follow=1 keeps the stream open with every create asked, answered or
// withdrawn. A follower is a client that can answer: while it holds the
// stream, a create waits for it.
func (e *Engine) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("follow") != "1" {
		httpjson.Write(w, http.StatusOK, map[string]any{"items": e.Waiting()})
		return
	}
	events, stop := e.Subscribe()
	defer stop()
	detach := e.Attach()
	defer detach()
	w.Header().Set("Content-Type", "application/x-ndjson")
	stream := newStream(w)
	for _, a := range e.Waiting() {
		if !stream.send(Event{Kind: EventAsked, Approval: &a}) {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev.Kind {
			case EventAsked, EventAnswered, EventWithdrawn:
				if !stream.send(ev) {
					return
				}
			}
		}
	}
}

// handleAnswer is POST /v1/approvals/{op}: {"answer": "allow" | "deny" |
// "remember"}, and optionally the client's name, which the other clients
// read beside the answer.
func (e *Engine) handleAnswer(w http.ResponseWriter, r *http.Request) {
	body, ok := httpjson.DecodeBody[struct {
		Answer string `json:"answer"`
		Client string `json:"client"`
	}](w, r)
	if !ok {
		return
	}
	by := strings.TrimSpace(body.Client)
	if by == "" {
		by = controlClient
	}
	if err := e.Answer(r.PathValue("op"), body.Answer, by); err != nil {
		writeControlError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stream writes one JSON value per line and flushes each.
type stream struct {
	w   http.ResponseWriter
	enc *json.Encoder
	rc  *http.ResponseController
}

func newStream(w http.ResponseWriter) *stream {
	w.WriteHeader(http.StatusOK)
	s := &stream{w: w, enc: json.NewEncoder(w), rc: http.NewResponseController(w)}
	_ = s.rc.Flush()
	return s
}

// send writes v, and reports whether the client is still there.
func (s *stream) send(v any) bool {
	if err := s.enc.Encode(v); err != nil {
		return false
	}
	return s.rc.Flush() == nil
}
