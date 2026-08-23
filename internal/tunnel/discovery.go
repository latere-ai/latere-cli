// Package tunnel implements the serve side of the Lux local-runtime
// reverse tunnel. `latere lux serve` dials lux.latere.ai over WSS,
// advertises the local runtime's models, and forwards inbound request
// streams to the local server. The Lux gateway routes a client's
// /local/v1 request back down the tunnel.
//
// The CLI is deliberately thin: it dials out, advertises a descriptor, and
// relays bytes. All policy (auth, gates, load-balancing) lives in luxd.
package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Runtime defaults: the local URL and discovery probe per recognized
// runtime. Every runtime speaks the openai-compat dialect; only the
// default port and the model-list probe differ. An unknown runtime is
// treated as generic openai-compat.
const (
	RuntimeOllama   = "ollama"
	RuntimeVLLM     = "vllm"
	RuntimeLMStudio = "lmstudio"
	RuntimeLlamaCPP = "llamacpp"
	RuntimeMLX      = "mlx"
	RuntimeOpenAI   = "openai-compat"

	DialectOpenAICompat = "openai-compat"
)

// DefaultURL returns the conventional local base URL for a runtime.
func DefaultURL(runtime string) string {
	switch runtime {
	case RuntimeOllama:
		return "http://localhost:11434"
	case RuntimeVLLM:
		return "http://localhost:8000"
	case RuntimeLMStudio:
		return "http://localhost:1234"
	case RuntimeLlamaCPP, RuntimeMLX:
		return "http://localhost:8080"
	default:
		return "http://localhost:11434"
	}
}

// discover enumerates the models the local runtime serves. Ollama exposes a
// richer /api/tags; every other runtime uses the generic OpenAI /v1/models.
// allow, when non-empty, filters the result to that set.
func discover(ctx context.Context, hc *http.Client, runtime, baseURL string, allow []string) ([]string, error) {
	base := strings.TrimRight(baseURL, "/")
	var models []string
	var err error
	if runtime == RuntimeOllama {
		models, err = discoverOllama(ctx, hc, base)
	} else {
		models, err = discoverOpenAI(ctx, hc, base)
	}
	if err != nil {
		return nil, err
	}
	if len(allow) == 0 {
		return models, nil
	}
	out := models[:0:0]
	for _, m := range models {
		if slices.Contains(allow, m) {
			out = append(out, m)
		}
	}
	return out, nil
}

func discoverOllama(ctx context.Context, hc *http.Client, base string) ([]string, error) {
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := getJSON(ctx, hc, base+"/api/tags", &body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		out = append(out, m.Name)
	}
	return out, nil
}

func discoverOpenAI(ctx context.Context, hc *http.Client, base string) ([]string, error) {
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, hc, base+"/v1/models", &body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

// Preflight verifies the local runtime answers before the tunnel opens,
// so a missing runtime surfaces as one clear availability message
// instead of a reconnect loop of dial errors. Returns the models the
// runtime reports (filtered by allow when non-empty).
func Preflight(ctx context.Context, runtime, upstreamURL string, allow []string) ([]string, error) {
	base := upstreamURL
	if base == "" {
		base = DefaultURL(runtime)
	}
	return discover(ctx, &http.Client{Timeout: 10 * time.Second}, runtime, base, allow)
}

func getJSON(ctx context.Context, hc *http.Client, url string, dst any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("reach local runtime at %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("local runtime %s returned %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dst)
}
