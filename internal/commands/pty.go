package commands

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/latere-ai/latere-cli/internal/api"
)

const (
	ptyFrameStdin  byte = 0x01
	ptyFrameStdout byte = 0x02
	ptyFrameResize byte = 0x03
	ptyFrameExit   byte = 0x04
	ptyFramePing   byte = 0x05
)

func newCeShellCmd() *cobra.Command {
	var (
		apiURL  string
		session string
	)
	cmd := &cobra.Command{
		Use:     "shell <name|id>",
		Aliases: []string{"attach"},
		Short:   "Open an interactive PTY shell inside a cella.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			code, err := attachShell(cmd.Context(), c, args[0], session)
			if err != nil {
				return err
			}
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override cella base URL")
	f.StringVar(&session, "session", "", "session id for the PTY attachment; generated if omitted")
	return cmd
}

func attachShell(ctx context.Context, c *api.Client, sandbox, session string) (int, error) {
	if session == "" {
		session = randomSessionID()
	}
	wsURL, err := ptyWebSocketURL(c.BaseURL, sandbox, session)
	if err != nil {
		return 0, err
	}

	header := http.Header{"User-Agent": []string{"latere-cli"}}
	if c.Token != "" {
		header.Set("Authorization", "Bearer "+c.Token)
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 30 * time.Second
	conn, resp, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
			if msg := strings.TrimSpace(string(b)); msg != "" {
				return 0, fmt.Errorf("open PTY websocket: %w: %s", err, msg)
			}
		}
		return 0, fmt.Errorf("open PTY websocket: %w", err)
	}
	defer conn.Close()

	stdinFD := int(os.Stdin.Fd())
	stdoutFD := int(os.Stdout.Fd())
	if term.IsTerminal(stdinFD) {
		oldState, err := term.MakeRaw(stdinFD)
		if err != nil {
			return 0, fmt.Errorf("set terminal raw mode: %w", err)
		}
		defer term.Restore(stdinFD, oldState)
	}

	var writeMu sync.Mutex
	writeFrame := func(kind byte, payload []byte) error {
		frame := make([]byte, 1+len(payload))
		frame[0] = kind
		copy(frame[1:], payload)
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(websocket.BinaryMessage, frame)
	}
	sendResize := func() error {
		cols, rows, err := term.GetSize(stdoutFD)
		if err != nil || cols <= 0 || rows <= 0 {
			cols, rows = 80, 24
		}
		var frame [4]byte
		binary.BigEndian.PutUint16(frame[0:2], clampUint16(cols))
		binary.BigEndian.PutUint16(frame[2:4], clampUint16(rows))
		return writeFrame(ptyFrameResize, frame[:])
	}
	_ = sendResize()

	done := make(chan struct{})
	defer close(done)

	if sigs := resizeSignals(); len(sigs) > 0 {
		resizeCh := make(chan os.Signal, 1)
		signal.Notify(resizeCh, sigs...)
		defer signal.Stop(resizeCh)
		go func() {
			for {
				select {
				case <-done:
					return
				case <-resizeCh:
					_ = sendResize()
				}
			}
		}()
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := os.Stdin.Read(buf)
			if n > 0 {
				if err := writeFrame(ptyFrameStdin, buf[:n]); err != nil {
					_ = conn.Close()
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := writeFrame(ptyFramePing, nil); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			default:
				return 0, fmt.Errorf("read PTY websocket: %w", err)
			}
		}
		if mt != websocket.BinaryMessage || len(data) == 0 {
			continue
		}
		switch data[0] {
		case ptyFrameStdout:
			if _, err := os.Stdout.Write(data[1:]); err != nil {
				return 0, err
			}
		case ptyFrameExit:
			if len(data) >= 5 {
				return int(int32(binary.BigEndian.Uint32(data[1:5]))), nil
			}
			return 0, nil
		case ptyFramePing:
			_ = writeFrame(ptyFramePing, nil)
		}
	}
}

func ptyWebSocketURL(baseURL, sandbox, session string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported API URL scheme %q", u.Scheme)
	}
	basePath := strings.TrimRight(u.Path, "/")
	baseRawPath := strings.TrimRight(u.EscapedPath(), "/")
	u.Path = basePath + "/v1/sandboxes/" + sandbox + "/sessions/" + session
	u.RawPath = baseRawPath + "/v1/sandboxes/" + url.PathEscape(sandbox) + "/sessions/" + url.PathEscape(session)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func randomSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "cli-" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("cli-%d", time.Now().UnixNano())
}

func clampUint16(v int) uint16 {
	if v < 0 {
		return 0
	}
	if v > 0xffff {
		return 0xffff
	}
	return uint16(v)
}
