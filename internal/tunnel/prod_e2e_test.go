package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestProdE2EServeAndCall is a real end-to-end test of `latere lux serve`
// against a live Lux server (production by default). It is opt-in because
// it exposes the local model to the target Lux and needs a real credential:
// it skips unless LATERE_LUX_E2E=1 and LATERE_LUX_TOKEN are set, and unless
// a local Ollama serving the model is reachable.
//
// Run it with, e.g.:
//
//	LATERE_LUX_E2E=1 \
//	LATERE_LUX_TOKEN="$(latere lux token)" \
//	go test ./internal/tunnel/ -run TestProdE2E -v
//
// Override LUX_API_URL to target staging, and LATERE_LUX_E2E_MODEL to use a
// different local model (default gemma4:latest).
func TestProdE2EServeAndCall(t *testing.T) {
	if os.Getenv("LATERE_LUX_E2E") != "1" {
		t.Skip("set LATERE_LUX_E2E=1 to run the live Lux e2e (it exposes a local model to the target)")
	}
	token := os.Getenv("LATERE_LUX_TOKEN")
	if token == "" {
		t.Skip("LATERE_LUX_E2E set but LATERE_LUX_TOKEN missing; provide a bearer (e.g. `latere lux token`)")
	}
	luxURL := os.Getenv("LUX_API_URL")
	if luxURL == "" {
		luxURL = "https://lux.latere.ai"
	}
	luxURL = strings.TrimRight(luxURL, "/")
	model := os.Getenv("LATERE_LUX_E2E_MODEL")
	if model == "" {
		model = "gemma4:latest"
	}

	// Require a reachable local Ollama serving the model.
	hc := &http.Client{Timeout: 5 * time.Second}
	if _, err := discover(context.Background(), hc, RuntimeOllama, DefaultURL(RuntimeOllama), []string{model}); err != nil {
		t.Skipf("local Ollama not serving %q: %v", model, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the real serve loop against the live Lux.
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- Run(ctx, Options{
			LuxURL:            luxURL,
			Bearer:            func(context.Context) (string, error) { return token, nil },
			Runtime:           RuntimeOllama,
			Models:            []string{model},
			NodeID:            "e2e-prod-" + NodeID(),
			HeartbeatInterval: 5 * time.Second,
			Out:               io.Discard,
		})
	}()

	// Wait until the model shows up as a live tunnel in the catalog,
	// confirming the tunnel registered through the live server.
	if !waitForCatalog(t, hc, luxURL, token, model, 30*time.Second) {
		t.Fatalf("model %q never appeared as local/ in %s/lux/v1/models within 30s "+
			"(is the tunnel feature enabled on the target and does the token carry llm.serve?)", model, luxURL)
	}

	// Call it through Lux and assert a real completion comes back.
	body := `{"model":"` + model + `","stream":false,"max_tokens":20,` +
		`"messages":[{"role":"user","content":"Reply with exactly: PONG"}]}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, luxURL+"/local/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("call /local: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/local status = %d: %s", resp.StatusCode, respBody)
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil || len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		t.Fatalf("no completion via prod tunnel: err=%v body=%s", err, respBody)
	}
	t.Logf("prod e2e: %s replied %q via %s/local/v1", model, out.Choices[0].Message.Content, luxURL)
}

func waitForCatalog(t *testing.T, hc *http.Client, luxURL, token, model string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, luxURL+"/lux/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := hc.Do(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && bytes.Contains(b, []byte(`"local/`+model+`"`)) {
				return true
			}
		}
		time.Sleep(time.Second)
	}
	return false
}
