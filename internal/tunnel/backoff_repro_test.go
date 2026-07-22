package tunnel

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// TestBackoffResetsAfterSuccessfulSession pins the reconnect contract: the
// delay grows only across consecutive failures to establish a tunnel. Once a
// session has been established (the descriptor is on the wire), a later drop
// restarts from the base delay. Without the reset, a long-lived tunnel that
// flaps once an hour keeps doubling until every reconnect waits the 30s cap.
//
// The base delay comes from Options so the assertion is a few hundred
// milliseconds of wall clock rather than seconds.
func TestBackoffResetsAfterSuccessfulSession(t *testing.T) {
	const base = 100 * time.Millisecond
	const wantSessions = 3

	// Fake local runtime: only the discovery probe is needed, no request is
	// ever forwarded.
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"m1"}]}`))
	}))
	defer local.Close()

	var mu sync.Mutex
	established := make([]time.Time, 0, wantSessions)
	reached := make(chan struct{})

	// Fake luxd: complete the handshake, then drop the connection. Every
	// session therefore reaches "established" before it fails.
	lux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"lux.tunnel.v1"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(-1)
		nc := websocket.NetConn(r.Context(), c, websocket.MessageBinary)
		sess, err := yamux.Server(nc, yamuxConfig())
		if err != nil {
			return
		}
		defer sess.Close()
		ctrl, err := sess.AcceptStream()
		if err != nil {
			return
		}
		if _, err := bufio.NewReader(ctrl).ReadBytes('\n'); err != nil {
			return
		}
		mu.Lock()
		established = append(established, time.Now())
		n := len(established)
		mu.Unlock()
		if n == wantSessions {
			close(reached)
		}
	}))
	defer lux.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Run(ctx, Options{
			LuxURL:            lux.URL,
			Bearer:            func(context.Context) (string, error) { return "test-bearer", nil },
			Runtime:           RuntimeOllama,
			UpstreamURL:       local.URL,
			NodeID:            "test-node",
			HeartbeatInterval: time.Hour,
			ReconnectBackoff:  base,
			Out:               io.Discard,
		})
	}()

	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		mu.Lock()
		n := len(established)
		mu.Unlock()
		t.Fatalf("only %d of %d sessions established before the deadline", n, wantSessions)
	}

	mu.Lock()
	stamps := append([]time.Time(nil), established...)
	mu.Unlock()

	gaps := make([]time.Duration, 0, len(stamps)-1)
	for i := 1; i < len(stamps); i++ {
		gaps = append(gaps, stamps[i].Sub(stamps[i-1]))
	}
	// Each reconnect follows an established-then-dropped session, so each
	// gap must be the base delay. A doubled delay is unambiguously over
	// 1.5x base; scheduling jitter is not.
	limit := base * 3 / 2
	for i, g := range gaps {
		if g > limit {
			t.Fatalf("reconnect delay grew after a successful session: gap %d = %s; want about %s each time (all observed: %v)",
				i+1, g.Round(time.Millisecond), base, gaps)
		}
	}
}
