package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"latere.ai/x/pkg/retry"
	"latere.ai/x/pkg/wait"
)

// fatalErr marks an error that reconnecting cannot fix (not signed in,
// missing capability, or the tunnel feature disabled on the server). Run
// surfaces it once and exits, instead of looping with backoff forever.
type fatalErr struct{ err error }

func (e fatalErr) Error() string { return e.err.Error() }
func (e fatalErr) Unwrap() error { return e.err }
func fatal(err error) error      { return fatalErr{err} }
func isFatal(err error) bool {
	var f fatalErr
	return errors.As(err, &f)
}

// Descriptor is the handshake contract advertised to the Lux gateway. It
// must match the gateway's tunnel descriptor byte for byte on the wire.
type Descriptor struct {
	NodeID  string   `json:"node_id"`
	Runtime string   `json:"runtime"`
	Dialect string   `json:"dialect"`
	BaseURL string   `json:"base_url"`
	Models  []string `json:"models"`
	Share   string   `json:"share"`
}

// Options configures a serve session.
type Options struct {
	// LuxURL is the Lux base URL (http/https); it is rewritten to ws/wss.
	LuxURL string
	// Bearer returns a fresh identity bearer for the dial and for the
	// heartbeat re-auth frame (so revocation takes effect within one
	// heartbeat). Called on each (re)connect and each heartbeat.
	Bearer func(ctx context.Context) (string, error)
	// Runtime, UpstreamURL, Models, Share, NodeID build the descriptor.
	Runtime     string
	UpstreamURL string
	Models      []string
	Share       string
	NodeID      string
	// HeartbeatInterval defaults to 10s (TTL/3 against luxd's 30s).
	HeartbeatInterval time.Duration
	// ReconnectBackoff is the delay before the first reconnect attempt; it
	// doubles across consecutive failures to establish a tunnel, up to 30s,
	// and returns to this value once a session has been established.
	// Defaults to 1s.
	ReconnectBackoff time.Duration
	// Out receives human-readable status lines.
	Out io.Writer
}

// Run serves the local runtime until ctx is canceled, redialing with
// backoff on connection loss. It is a long-running, outbound-only client:
// it opens no inbound listener.
func Run(ctx context.Context, opts Options) error {
	if opts.Runtime == "" {
		opts.Runtime = RuntimeOllama
	}
	if opts.UpstreamURL == "" {
		opts.UpstreamURL = DefaultURL(opts.Runtime)
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 10 * time.Second
	}
	if opts.Share == "" {
		opts.Share = "owner"
	}
	if opts.ReconnectBackoff <= 0 {
		opts.ReconnectBackoff = time.Second
	}

	policy := retry.Policy{Base: opts.ReconnectBackoff, Max: 30 * time.Second}
	failures := 0
	for {
		established, err := runSession(ctx, opts)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The delay grows only across consecutive failures to establish a
		// tunnel. A session that came up and later dropped is not evidence
		// that Lux is down, so it restarts from the base delay; otherwise a
		// tunnel that flaps occasionally over a long run would creep up to
		// the 30s cap and stay there.
		if established {
			failures = 0
		}
		// A non-retryable error (not signed in, not permitted to serve,
		// feature disabled) returns immediately so the user sees one clear message
		// instead of an endless reconnect loop.
		if isFatal(err) {
			return err
		}
		failures++
		delay := policy.Delay(failures)
		if err != nil {
			fprintf(opts.Out, "tunnel: disconnected (%v); reconnecting in %s\n", err, delay.Round(time.Millisecond))
		}
		if err := wait.Sleep(ctx, delay); err != nil {
			return err
		}
	}
}

// runSession runs one tunnel connection to completion. established reports
// whether the tunnel ever became usable (the descriptor reached luxd), which
// is what Run needs to tell a failed dial apart from a dropped session.
func runSession(ctx context.Context, opts Options) (established bool, err error) {
	hc := &http.Client{}

	models, err := discover(ctx, hc, opts.Runtime, opts.UpstreamURL, opts.Models)
	if err != nil {
		return established, err
	}
	if len(models) == 0 {
		return established, fmt.Errorf("no models found at %s (is %s running?)", opts.UpstreamURL, opts.Runtime)
	}

	bearer, err := opts.Bearer(ctx)
	if err != nil {
		// No usable identity (e.g. not signed in). Retrying will not help.
		return established, fatal(err)
	}

	wsURL := toWS(opts.LuxURL) + "/lux/v1/tunnel"
	// resp is read for its status when the handshake fails and is never
	// closed here: websocket.Dial leaves it nil on success, where the
	// connection owns the underlying socket, and has already closed and
	// buffered it on failure.
	c, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{ //nolint:bodyclose
		Subprotocols: []string{"lux.tunnel.v1"},
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + bearer}},
	})
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusNotFound:
				return established, fatal(fmt.Errorf("the local-model tunnel is not enabled on %s yet. Ask your operator to turn it on (LUX_TUNNEL_ENABLED)", opts.LuxURL))
			case http.StatusUnauthorized, http.StatusForbidden:
				return established, fatal(errors.New("your login may not serve models here. Run `latere login` to refresh your session, then try again"))
			}
		}
		return established, fmt.Errorf("dial %s: %w", wsURL, err)
	}
	defer func() { _ = c.CloseNow() }()
	c.SetReadLimit(-1)

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	nc := websocket.NetConn(sessCtx, c, websocket.MessageBinary)
	sess, err := yamux.Client(nc, yamuxConfig())
	if err != nil {
		return established, err
	}
	defer func() { _ = sess.Close() }()

	ctrl, err := sess.OpenStream()
	if err != nil {
		return established, err
	}
	desc := Descriptor{
		NodeID:  opts.NodeID,
		Runtime: opts.Runtime,
		Dialect: DialectOpenAICompat,
		BaseURL: opts.UpstreamURL,
		Models:  models,
		Share:   opts.Share,
	}
	line, err := json.Marshal(desc)
	if err != nil {
		return established, fmt.Errorf("encode descriptor: %w", err)
	}
	if _, err := ctrl.Write(append(line, '\n')); err != nil {
		return established, err
	}
	fprintf(opts.Out, "tunnel: connected; serving %d model(s) from %s (%s), share=%s\n",
		len(models), opts.UpstreamURL, opts.Runtime, opts.Share)
	for _, m := range models {
		fprintf(opts.Out, "  - %s  (call as local/%s via Lux)\n", m, m)
	}
	// The descriptor is on the wire, so luxd can route to this node: the
	// tunnel is usable. Recording it here, at the single point where that
	// becomes true, keeps every later exit path (session drop, accept error)
	// from having to remember to report it.
	established = true

	go heartbeatLoop(sessCtx, ctrl, opts)

	srv := &forwarder{ctx: sessCtx, client: hc, upstream: strings.TrimRight(opts.UpstreamURL, "/"), out: opts.Out}
	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return established, err // session closed
		}
		go srv.handle(stream)
	}
}

// heartbeatLoop refreshes liveness and re-presents a fresh bearer so luxd
// can drop the tunnel within one heartbeat if the identity is revoked.
func heartbeatLoop(ctx context.Context, ctrl net.Conn, opts Options) {
	t := time.NewTicker(opts.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			frame := map[string]string{}
			if tok, err := opts.Bearer(ctx); err == nil {
				frame["token"] = tok
			}
			line, err := json.Marshal(frame)
			if err != nil {
				// frame is a string map, so this cannot happen. Skipping the
				// beat rather than returning keeps a hypothetical encoding
				// fault from tearing down a working tunnel; luxd drops the
				// node only after several missed heartbeats.
				continue
			}
			if _, err := ctrl.Write(append(line, '\n')); err != nil {
				return
			}
		}
	}
}

// forwarder relays one inbound request stream to the local runtime. Because
// it builds the target URL from its own configured upstream (never from the
// request's host), it can only ever reach that one server: the egress
// allowlist is structural, not a check.
type forwarder struct {
	ctx      context.Context
	client   *http.Client
	upstream string
	out      io.Writer // traffic log sink (status lines); nil disables logging
}

// maxRequestBytes caps how much of an inbound request body the forwarder
// buffers, matching the bounded reads elsewhere in this package
// (discovery.go, release.go). It sits well above realistic
// chat-completion request sizes; the body arrives from luxd, the trusted
// intermediary, so this guards against a malformed or truncated read
// rather than a hostile flood. A body past the cap is refused with 413, never
// trimmed to fit.
const maxRequestBytes = 8 << 20 // 8 MiB

func (f *forwarder) handle(stream net.Conn) {
	defer func() { _ = stream.Close() }()
	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		return
	}

	// Buffer the body so it can be both forwarded and peeked for the model
	// id in the traffic log. Chat-completion bodies are small next to the
	// model weights, so reading fully is cheap. Bound the read and surface a
	// read error loudly: forwarding a silently truncated-but-self-consistent
	// body (ContentLength = len(body)) would relay a corrupt request to the
	// upstream instead of failing.
	started := time.Now()
	var body []byte
	if req.Body != nil {
		// Read one byte past the cap: a plain LimitReader at the cap returns
		// a clean io.EOF, so an oversized body would look like a complete
		// short one and be relayed with a matching ContentLength.
		body, err = io.ReadAll(io.LimitReader(req.Body, maxRequestBytes+1))
		_ = req.Body.Close()
		if err != nil {
			writeError(stream, err)
			f.logTraffic(req.Method, req.URL.RequestURI(), peekModel(body), 0, time.Since(started), err)
			return
		}
		if len(body) > maxRequestBytes {
			err := fmt.Errorf("request body exceeds the %d byte limit", maxRequestBytes)
			writeErrorStatus(stream, http.StatusRequestEntityTooLarge, "local.request_too_large", err)
			f.logTraffic(req.Method, req.URL.RequestURI(), peekModel(body), http.StatusRequestEntityTooLarge, time.Since(started), err)
			return
		}
	}
	model := peekModel(body)
	path := req.URL.RequestURI()

	target := f.upstream + path
	out, err := http.NewRequestWithContext(f.ctx, req.Method, target, bytes.NewReader(body))
	if err != nil {
		writeError(stream, err)
		f.logTraffic(req.Method, path, model, 0, time.Since(started), err)
		return
	}
	out.Header = req.Header.Clone()
	out.Header.Del("Connection")
	out.ContentLength = int64(len(body))

	resp, err := f.client.Do(out)
	if err != nil {
		writeError(stream, err)
		f.logTraffic(req.Method, path, model, http.StatusBadGateway, time.Since(started), err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_ = resp.Write(stream)
	f.logTraffic(req.Method, path, model, resp.StatusCode, time.Since(started), nil)
}

// peekModel pulls the OpenAI-compatible "model" field out of a request body
// for the traffic log; "" when absent or unparseable.
func peekModel(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model
}

// logTraffic prints one served-request line (method, path, model, status,
// latency) to the serve command's status sink. No-op when out is nil.
func (f *forwarder) logTraffic(method, path, model string, status int, dur time.Duration, err error) {
	if f.out == nil {
		return
	}
	if model == "" {
		model = "-"
	}
	ts := time.Now().Format("15:04:05")
	if err != nil {
		fprintf(f.out, "%s  %s %s  model=%s  error: %v  (%dms)\n",
			ts, method, path, model, err, dur.Milliseconds())
		return
	}
	fprintf(f.out, "%s  %s %s  model=%s  %d  (%dms)\n",
		ts, method, path, model, status, dur.Milliseconds())
}

// writeError reports a failure to reach or read from the local runtime.
func writeError(w io.Writer, err error) {
	writeErrorStatus(w, http.StatusBadGateway, "local.unreachable", err)
}

// writeErrorStatus writes one error response in the OpenAI-compatible
// envelope. Refusals raised by the forwarder itself (an oversized body) are
// not upstream-reachability failures and must not be reported as 502.
func writeErrorStatus(w io.Writer, status int, code string, err error) {
	resp := &http.Response{
		StatusCode: status,
		ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, code, err.Error()))),
	}
	_ = resp.Write(w)
}

// toWS rewrites an http(s) base URL to ws(s).
func toWS(base string) string {
	base = strings.TrimRight(base, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://")
	default:
		return base
	}
}
