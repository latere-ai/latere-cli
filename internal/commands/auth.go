package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate against auth.latere.ai.",
	}
	cmd.AddCommand(newAuthLoginCmd())
	cmd.AddCommand(newAuthWhoamiCmd())
	cmd.AddCommand(newAuthPrintTokenCmd())
	cmd.AddCommand(newAuthLogoutCmd())
	return cmd
}

// newAuthPrintTokenCmd prints the saved access token to stdout so it
// can be embedded in shell scripts: `TOKEN=$(latere auth print-token)`.
// Stays on stdout (without a trailing newline guaranteed by Println)
// so command substitution gives a clean string.
func newAuthPrintTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "print-token",
		Short: "Print the saved access token to stdout (for use in scripts).",
		Long: `Print the OAuth access token from ~/.config/latere/token.json.

Useful for piping into shell tools without depending on jq:

    TOKEN=$(latere auth print-token)
    curl -H "Authorization: Bearer $TOKEN" https://cella.latere.ai/v1/sandboxes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			tok, err := api.LoadToken("")
			if err != nil {
				return err
			}
			if tok.AccessToken == "" {
				return api.ErrNoToken
			}
			fmt.Println(tok.AccessToken)
			return nil
		},
	}
}

func newAuthLoginCmd() *cobra.Command {
	var (
		token    string
		apiURL   string
		authURL  string
		clientID string
		scopes   string
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in via OAuth2 device-code (or paste a token with --token).",
		Long: `Sign in to Latere.

By default, login starts the OAuth2 device-code flow against
auth.latere.ai: it prints a short user code and a URL, you visit the
URL in any browser to approve, and the CLI then polls until the
approval lands. The resulting access token is written to
~/.config/latere/token.json with 0600 perms; the MCP server
(sandbox-mcp) reads the same file.

For unattended setups (CI, scripts), pass --token to skip the device
flow and store an access token directly.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Token-paste fast path: --token wins, or stdin pipe falls
			// back to it. The device flow only kicks in for an
			// interactive terminal with no --token.
			if t := strings.TrimSpace(token); t != "" {
				return saveAndVerify(ctx, apiURL, t)
			}
			if stat, _ := os.Stdin.Stat(); (stat.Mode() & os.ModeCharDevice) == 0 {
				b, err := readAll(os.Stdin)
				if err != nil {
					return err
				}
				if t := strings.TrimSpace(b); t != "" {
					return saveAndVerify(ctx, apiURL, t)
				}
			}

			return runDeviceFlow(ctx, deviceFlowOpts{
				AuthURL:  authURL,
				APIURL:   apiURL,
				ClientID: clientID,
				Scopes:   scopes,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&token, "token", "", "skip device flow; store an access token directly")
	f.StringVar(&apiURL, "api-url", "", "override sandboxd base URL (default https://cella.latere.ai)")
	f.StringVar(&authURL, "auth-url", "", "override auth base URL (default https://auth.latere.ai)")
	f.StringVar(&clientID, "client-id", "latere-cli", "OAuth client_id used for the device-code request")
	f.StringVar(&scopes, "scopes", "openid email profile read:sandbox write:sandbox exec:sandbox",
		"space-delimited scope list")
	return cmd
}

// saveAndVerify stores the token and confirms it by listing sandboxes.
// Shared by the --token fast path and the device-code happy path.
func saveAndVerify(ctx context.Context, apiURL, token string) error {
	if err := api.SaveToken("", api.Token{
		AccessToken: token,
		TokenType:   "Bearer",
		IssuedAt:    time.Now().UTC(),
	}); err != nil {
		return err
	}
	c := api.NewClient(apiURL)
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var ignored any
	if err := c.GetJSON(verifyCtx, "/v1/sandboxes", &ignored); err != nil {
		_ = api.ClearToken("")
		return fmt.Errorf("token rejected by sandboxd: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Logged in. Token saved to %s\n", api.TokenPath())
	return nil
}

// ---- device-code flow ----

type deviceFlowOpts struct {
	AuthURL, APIURL, ClientID, Scopes string
}

type deviceCodeResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResp struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

func runDeviceFlow(ctx context.Context, opts deviceFlowOpts) error {
	authBase := opts.AuthURL
	if authBase == "" {
		authBase = inferAuthURL(opts.APIURL)
	}
	authBase = strings.TrimRight(authBase, "/")

	// 1. Initiate.
	form := url.Values{}
	form.Set("client_id", opts.ClientID)
	if opts.Scopes != "" {
		form.Set("scope", opts.Scopes)
	}
	httpc := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		authBase+"/device/code", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("device/code: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return fmt.Errorf("device/code %d: %s", resp.StatusCode, b)
	}
	var dc deviceCodeResp
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return fmt.Errorf("device/code decode: %w", err)
	}

	// 2. Surface the user code.
	verify := dc.VerificationURIComplete
	if verify == "" {
		verify = dc.VerificationURI
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  To sign in, open this URL:\n\n      %s\n\n", verify)
	fmt.Fprintf(os.Stderr, "  And confirm the code:\n\n      %s\n\n", dc.UserCode)
	fmt.Fprintln(os.Stderr, "  Waiting for approval...")

	// 3. Poll /token until terminal status.
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		if time.Now().After(deadline) {
			return errors.New("device code expired before approval")
		}

		tform := url.Values{}
		tform.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		tform.Set("device_code", dc.DeviceCode)
		tform.Set("client_id", opts.ClientID)
		treq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			authBase+"/token", strings.NewReader(tform.Encode()))
		if err != nil {
			return err
		}
		treq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		tresp, err := httpc.Do(treq)
		if err != nil {
			return fmt.Errorf("token poll: %w", err)
		}
		var body tokenResp
		_ = json.NewDecoder(tresp.Body).Decode(&body)
		_ = tresp.Body.Close()

		switch body.Error {
		case "":
			if body.AccessToken == "" {
				return errors.New("token endpoint returned no access_token")
			}
			// Best-effort: trade the auth-issued token for a
			// cella-issued bearer. Falls back to the auth token during
			// the deprecation window so installs without the cella
			// catalog keep working.
			if cellaTok, err := exchangeForCellaToken(ctx, opts, body.AccessToken); err == nil && cellaTok != "" {
				return saveAndVerify(ctx, opts.APIURL, cellaTok)
			} else if err != nil {
				fmt.Fprintf(os.Stderr, "  cella token exchange unavailable (%v); using auth-issued token\n", err)
			}
			return saveAndVerify(ctx, opts.APIURL, body.AccessToken)
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "expired_token":
			return errors.New("device code expired before approval")
		case "access_denied":
			return errors.New("user denied the request")
		default:
			return fmt.Errorf("device-code login failed: %s (%s)", body.Error, body.ErrorDescription)
		}
	}
}

// exchangeForCellaToken trades an auth-issued user JWT for a cella
// bearer token. The CLI mints a short-TTL actor token at auth, then
// exchanges it at cella's /v1/tokens/exchange. Either step may fail
// (older auth without /actor-tokens, older cella without the token
// catalog); the caller falls back to the auth token in that case.
func exchangeForCellaToken(ctx context.Context, opts deviceFlowOpts, authToken string) (string, error) {
	authBase := opts.AuthURL
	if authBase == "" {
		authBase = inferAuthURL(opts.APIURL)
	}
	authBase = strings.TrimRight(authBase, "/")
	apiBase := strings.TrimRight(opts.APIURL, "/")
	if apiBase == "" {
		apiBase = "https://cella.latere.ai"
	}

	httpc := &http.Client{Timeout: 15 * time.Second}

	// 1. Mint an actor token at auth.
	actorAud := strings.TrimPrefix(strings.TrimPrefix(apiBase, "https://"), "http://")
	body, _ := json.Marshal(map[string]any{"audience": actorAud, "ttl_seconds": 60})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authBase+"/actor-tokens", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return "", fmt.Errorf("actor-tokens %d: %s", resp.StatusCode, b)
	}
	var actor struct {
		ActorToken string `json:"actor_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&actor); err != nil || actor.ActorToken == "" {
		return "", fmt.Errorf("actor-tokens: empty response")
	}

	// 2. Exchange the actor token at cella.
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "CLI"
	}
	body, _ = json.Marshal(map[string]any{"label": "CLI on " + hostname})
	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v1/tokens/exchange", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+actor.ActorToken)
	resp2, err := httpc.Do(req2)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<14))
		return "", fmt.Errorf("tokens/exchange %d: %s", resp2.StatusCode, b)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("tokens/exchange: empty response")
	}
	return out.AccessToken, nil
}

// inferAuthURL maps a sandboxd URL like https://cella.latere.ai to the
// auth base https://auth.latere.ai. Falls back to a sane default for
// the public deployment if the API URL isn't a known shape.
func inferAuthURL(apiURL string) string {
	if apiURL == "" {
		return "https://auth.latere.ai"
	}
	if u, err := url.Parse(apiURL); err == nil && u.Host != "" {
		// Replace the leading host label.
		parts := strings.SplitN(u.Host, ".", 2)
		if len(parts) == 2 {
			u.Host = "auth." + parts[1]
			u.Path = ""
			return u.String()
		}
	}
	return "https://auth.latere.ai"
}

// silence unused import warnings on older toolchains where bufio/io were
// only used in pre-device-code code paths.
var _ = bufio.NewReader


func newAuthWhoamiCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Print the current principal.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := api.NewClient(apiURL)
			if err := c.MustRequireAuth(); err != nil {
				return err
			}
			// sandboxd doesn't have /me; auth does. Hit auth's
			// /tokeninfo (best-effort issuer URL inferred from the
			// API URL by replacing cella → auth).
			authURL := strings.Replace(c.BaseURL, "cella.", "auth.", 1)
			req := *c
			req.BaseURL = authURL
			var info struct {
				Sub           string   `json:"sub"`
				Email         *string  `json:"email,omitempty"`
				PrincipalType string   `json:"principal_type"`
				OrgID         *string  `json:"org_id,omitempty"`
				Scopes        []string `json:"scopes"`
				ClientID      string   `json:"client_id,omitempty"`
			}
			if err := req.GetJSON(cmd.Context(), "/tokeninfo", &info); err != nil {
				return err
			}
			fmt.Printf("sub:           %s\n", info.Sub)
			if info.Email != nil && *info.Email != "" {
				fmt.Printf("email:         %s\n", *info.Email)
			}
			fmt.Printf("principal:     %s\n", info.PrincipalType)
			if info.OrgID != nil && *info.OrgID != "" {
				fmt.Printf("org_id:        %s\n", *info.OrgID)
			}
			if info.ClientID != "" {
				fmt.Printf("client_id:     %s\n", info.ClientID)
			}
			if len(info.Scopes) > 0 {
				fmt.Printf("scopes:        %s\n", strings.Join(info.Scopes, " "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	return cmd
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear ~/.config/latere/token.json.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := api.ClearToken(""); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Logged out.")
			return nil
		},
	}
}

// readAll reads all of r into a string. Bounded at 64KiB to keep a
// noisy stdin from filling memory.
func readAll(r interface {
	Read([]byte) (int, error)
}) (string, error) {
	const max = 64 << 10
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > max {
				return "", errors.New("token input too large")
			}
		}
		if err != nil {
			break
		}
	}
	return string(buf), nil
}
