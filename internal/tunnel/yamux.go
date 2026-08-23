package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/latere-ai/latere-cli/internal/config"
)

// yamuxConfig mirrors the luxd side: keepalive on so a dead peer is
// detected when idle, logging silenced.
func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.LogOutput = io.Discard
	return cfg
}

// NodeID returns a stable per-machine node id, persisted under the latere
// config dir so a reconnect reuses it and overwrites its own registry
// member instead of creating a duplicate. A random id is generated and
// saved on first use.
func NodeID() string {
	p := nodeIDPath()
	if p != "" {
		if b, err := os.ReadFile(p); err == nil {
			if s := string(b); len(s) > 0 {
				return s
			}
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "node-unknown"
	}
	id := "node-" + hex.EncodeToString(buf)
	if p != "" {
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte(id), 0o600)
	}
	return id
}

func nodeIDPath() string {
	return config.Path("tunnel-node-id")
}
