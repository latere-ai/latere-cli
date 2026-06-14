package tunnel

import (
	"bufio"
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

// Descriptor is the handshake contract advertised to luxd (spec 18 Layer
// 2). It must match latere-ai/lux internal/tunnel.Descriptor on the wire.
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

	backoff := time.Second
	for {
		err := runSession(ctx, opts)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A non-retryable error (not signed in, missing llm.serve, feature
		// disabled) returns immediately so the user sees one clear message
		// instead of an endless reconnect loop.
		if isFatal(err) {
			return err
		}
		if err != nil {
			fmt.Fprintf(opts.Out, "tunnel: disconnected (%v); reconnecting in %s\n", err, backoff)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func runSession(ctx context.Context, opts Options) error {
	hc := &http.Client{}

	models, err := discover(ctx, hc, opts.Runtime, opts.UpstreamURL, opts.Models)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return fmt.Errorf("no models found at %s (is %s running?)", opts.UpstreamURL, opts.Runtime)
	}

	bearer, err := opts.Bearer(ctx)
	if err != nil {
		// No usable identity (e.g. not signed in). Retrying will not help.
		return fatal(err)
	}

	wsURL := toWS(opts.LuxURL) + "/lux/v1/tunnel"
	c, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"lux.tunnel.v1"},
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + bearer}},
	})
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusNotFound:
				return fatal(fmt.Errorf("the local-model tunnel is not enabled on %s yet. Ask your operator to turn it on (LUX_TUNNEL_ENABLED).", opts.LuxURL))
			case http.StatusUnauthorized, http.StatusForbidden:
				return fatal(fmt.Errorf("your login may not serve models here (it needs the llm.serve scope). Run `latere auth login` to refresh your scopes, then try again."))
			}
		}
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	defer c.CloseNow()
	c.SetReadLimit(-1)

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	nc := websocket.NetConn(sessCtx, c, websocket.MessageBinary)
	sess, err := yamux.Client(nc, yamuxConfig())
	if err != nil {
		return err
	}
	defer sess.Close()

	ctrl, err := sess.OpenStream()
	if err != nil {
		return err
	}
	desc := Descriptor{
		NodeID:  opts.NodeID,
		Runtime: opts.Runtime,
		Dialect: DialectOpenAICompat,
		BaseURL: opts.UpstreamURL,
		Models:  models,
		Share:   opts.Share,
	}
	line, _ := json.Marshal(desc)
	if _, err := ctrl.Write(append(line, '\n')); err != nil {
		return err
	}
	fmt.Fprintf(opts.Out, "tunnel: connected; serving %d model(s) from %s (%s), share=%s\n",
		len(models), opts.UpstreamURL, opts.Runtime, opts.Share)
	for _, m := range models {
		fmt.Fprintf(opts.Out, "  - %s  (call as local/%s via Lux)\n", m, m)
	}

	go heartbeatLoop(sessCtx, ctrl, opts)

	srv := &forwarder{ctx: sessCtx, client: hc, upstream: strings.TrimRight(opts.UpstreamURL, "/")}
	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return err // session closed
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
			line, _ := json.Marshal(frame)
			if _, err := ctrl.Write(append(line, '\n')); err != nil {
				return
			}
		}
	}
}

// forwarder relays one inbound request stream to the local runtime. Because
// it builds the target URL from its own configured upstream (never from the
// request's host), it can only ever reach that one server: the egress
// allowlist is structural, not a check (spec 18 security).
type forwarder struct {
	ctx      context.Context
	client   *http.Client
	upstream string
}

func (f *forwarder) handle(stream net.Conn) {
	defer stream.Close()
	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		return
	}
	target := f.upstream + req.URL.RequestURI()
	out, err := http.NewRequestWithContext(f.ctx, req.Method, target, req.Body)
	if err != nil {
		writeError(stream, err)
		return
	}
	out.Header = req.Header.Clone()
	out.Header.Del("Connection")
	out.ContentLength = req.ContentLength

	resp, err := f.client.Do(out)
	if err != nil {
		writeError(stream, err)
		return
	}
	defer resp.Body.Close()
	_ = resp.Write(stream)
}

func writeError(w io.Writer, err error) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			fmt.Sprintf(`{"error":{"code":"local.unreachable","message":%q}}`, err.Error()))),
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
