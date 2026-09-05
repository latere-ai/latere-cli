// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"latere.ai/x/pkg/otel"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"latere.ai/x/pkg/authkit"
	"latere.ai/x/pkg/oidc"

	"github.com/latere-ai/latere-cli/internal/api"
)

// newAuthCmd is a hidden back-compat alias: the session verbs live at the
// top level (latere login/whoami/print-token/logout/org). Children are built
// from the same factories as the top-level verbs so behavior cannot drift.
// Scripts written against `latere auth <verb>` keep working silently; remove
// the alias in a later major version.
func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "auth",
		Short:  "Deprecated alias for the top-level session commands.",
		Hidden: true,
	}
	cmd.AddCommand(newAuthLoginCmd())
	cmd.AddCommand(newAuthWhoamiCmd())
	cmd.AddCommand(newAuthPrintTokenCmd())
	cmd.AddCommand(newAuthLogoutCmd())
	cmd.AddCommand(newAuthOrgCmd())
	return cmd
}

// newOrgCmd is the top-level organization-context verb. With no argument it
// prints the active context of the saved token; with an org UUID (or
// --personal) it switches the context via the refresh-token grant, so the
// user does not need to re-run device-code login.
func newOrgCmd() *cobra.Command {
	var authURL, clientID string
	var personal bool
	cmd := &cobra.Command{
		Use:   "org [org-uuid]",
		Short: "Show or switch the active organization context.",
		Long: `Show or switch which organization the saved token is scoped to.

    latere org                # show the active context
    latere org <org-uuid>     # switch to <org-uuid>
    latere org --personal     # switch to the personal context

Switch uses the refresh-token grant; the user does not need to
re-run device-code login.`,
		Example: `  latere org
  latere org 3fa85f64-5717-4562-b3fc-2c963f66afa6
  latere org --personal`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if personal && len(args) == 1 {
				return errors.New("--personal and an org id are mutually exclusive")
			}
			if !personal && len(args) == 0 {
				return showOrgContext(cmd)
			}
			orgID := ""
			if len(args) == 1 {
				orgID = args[0]
			}
			return switchOrgContext(cmd, authURL, clientID, orgID)
		},
	}
	cmd.Flags().StringVar(&authURL, "auth-url", "", "auth service base URL (default $AUTH_URL or https://auth.latere.ai)")
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth client id (default $AUTH_CLIENT_ID or latere-cli)")
	cmd.Flags().BoolVar(&personal, "personal", false, "switch to the personal context")
	return cmd
}

// newAuthOrgCmd keeps `latere auth org switch` working under the hidden
// alias. It shares switchOrgContext with the top-level org verb.
func newAuthOrgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "org",
		Short:  "Deprecated alias for `latere org`.",
		Hidden: true,
	}
	cmd.AddCommand(newAuthOrgSwitchCmd())
	return cmd
}

func newAuthOrgSwitchCmd() *cobra.Command {
	var authURL, clientID string
	var personal bool
	cmd := &cobra.Command{
		Use:   "switch <org-uuid>",
		Short: "Switch the active org context using the saved refresh token.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			orgID := ""
			if len(args) == 1 {
				orgID = args[0]
			}
			if personal {
				orgID = ""
			}
			return switchOrgContext(cmd, authURL, clientID, orgID)
		},
	}
	cmd.Flags().StringVar(&authURL, "auth-url", "", "auth service base URL (default $AUTH_URL or https://auth.latere.ai)")
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth client id (default $AUTH_CLIENT_ID or latere-cli)")
	cmd.Flags().BoolVar(&personal, "personal", false, "switch to the personal context (equivalent to `switch \"\"`)")
	return cmd
}

// showOrgContext prints the active context without a network call: the org
// scope is stamped into the JWT claims at issue time. Prints "personal" or
// the org UUID, bare, so it is scriptable.
func showOrgContext(cmd *cobra.Command) error {
	tok, err := api.LoadAuthToken()
	if err != nil {
		tok, err = api.LoadToken("")
		if err != nil {
			return err
		}
	}
	info, err := principalFromJWT(tok.AccessToken)
	if err != nil {
		return err
	}
	if info.OrgID == "" {
		fprintln(cmd.OutOrStdout(), "personal")
	} else {
		fprintln(cmd.OutOrStdout(), info.OrgID)
	}
	return nil
}

// switchOrgContext re-scopes the saved token to orgID (empty = personal)
// using the refresh-token grant with `org_id=<uuid>`.
func switchOrgContext(cmd *cobra.Command, authURL, clientID, orgID string) error {
	tok, err := api.LoadAuthToken()
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return errors.New("no refresh token on file; run `latere login` first")
	}

	authBase := authURL
	if authBase == "" {
		authBase = os.Getenv("AUTH_URL")
		if authBase == "" {
			authBase = "https://auth.latere.ai"
		}
	}
	authBase = strings.TrimRight(authBase, "/")
	cid := clientID
	if cid == "" {
		cid = os.Getenv("AUTH_CLIENT_ID")
		if cid == "" {
			cid = "latere-cli"
		}
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {cid},
		"org_id":        {orgID},
	}
	req, err := http.NewRequestWithContext(cmd.Context(),
		http.MethodPost,
		authBase+"/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	httpc := &http.Client{Timeout: 15 * time.Second, Transport: otel.Transport(nil)}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("token endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token endpoint: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var got struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return fmt.Errorf("decode token: %w", err)
	}
	if got.AccessToken == "" {
		return errors.New("token endpoint returned no access_token")
	}

	expiry := time.Now().Add(time.Duration(got.ExpiresIn) * time.Second).UTC()
	if got.RefreshToken == "" {
		got.RefreshToken = tok.RefreshToken
	}
	if err := api.SaveAuthToken(api.Token{
		AccessToken:  got.AccessToken,
		RefreshToken: got.RefreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    expiry,
		IssuedAt:     time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("save auth token: %w", err)
	}
	if err := replaceCellaOrgToken(cmd.Context(), authBase, got.AccessToken); err != nil {
		return fmt.Errorf("auth context changed, but Cella credentials could not be updated: %w; retry the org switch or run `latere login`", err)
	}
	if orgID == "" {
		fprintln(cmd.ErrOrStderr(), "Switched to personal context.")
	} else {
		fprintf(cmd.ErrOrStderr(), "Switched to org %s.\n", orgID)
	}
	return nil
}

// replaceCellaOrgToken discards the previous scope before exchanging the new
// root token. If exchange fails, later commands must not use the old scope.
func replaceCellaOrgToken(ctx context.Context, authBase, rootToken string) error {
	if err := api.ClearToken(""); err != nil {
		return fmt.Errorf("remove previous Cella token: %w", err)
	}
	cellaBase := api.NewClient("").BaseURL
	token, err := exchangeForCellaToken(ctx, deviceFlowOpts{AuthURL: authBase, APIURL: cellaBase}, rootToken)
	if err != nil {
		return err
	}
	return api.SaveToken("", api.Token{
		AccessToken: token,
		TokenType:   "Bearer",
		IssuedAt:    time.Now().UTC(),
	})
}

// newAuthPrintTokenCmd prints the saved access token to stdout so it
// can be embedded in shell scripts: `TOKEN=$(latere print-token)`.
// Stays on stdout (without a trailing newline guaranteed by Println)
// so command substitution gives a clean string.
func newAuthPrintTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "print-token",
		Short: "Print the saved access token to stdout (for use in scripts).",
		Long: `Print the OAuth access token from ~/.config/latere/token.json.

Useful for piping into shell tools without depending on jq:

    TOKEN=$(latere print-token)
    curl -H "Authorization: Bearer $TOKEN" https://cella.latere.ai/v1/sandboxes`,
		Example: `  TOKEN=$(latere print-token)
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
		token     string
		apiURL    string
		authURL   string
		clientID  string
		scopes    string
		personal  bool
		orgID     string
		noBrowser bool
		noGit     bool
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in via OAuth2 device-code (or paste a token with --token).",
		Long: `Sign in to Latere.

By default, login starts the OAuth2 device-code flow against
auth.latere.ai: it prints a short user code and a URL, you visit the
URL in any browser to approve, choose the Personal or Organization
context for the token, and the CLI then polls until the approval lands.
The resulting access token is written to ~/.config/latere/token.json
with 0600 perms.

Use --personal or --org-id to preselect the token context from the
terminal. Re-run login with a different context to switch which cellas
the CLI can list and operate.

After a successful login the CLI also wires git's credential helper for
drive.latere.ai (idempotent, scoped to that host only), so plain
'git clone https://drive.latere.ai/git/me/<repo>.git' works with no
token in the URL. Pass --no-git to leave your git config untouched;
'latere git-credential setup --remove' undoes the wiring later.

For unattended setups (CI, scripts), pass --token to skip the device
flow and store an access token directly.`,
		Example: `  latere login
  latere login --personal
  latere login --org-id org_123
  latere login --no-browser
  latere login --no-git
  latere login --token "$LATERE_TOKEN"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if personal && strings.TrimSpace(orgID) != "" {
				return errors.New("--personal and --org-id are mutually exclusive")
			}

			// Token-paste fast path: --token wins, or stdin pipe falls
			// back to it. The device flow only kicks in for an
			// interactive terminal with no --token.
			login := func() error {
				if t := strings.TrimSpace(token); t != "" {
					return loginWithPastedToken(ctx, apiURL, t)
				}
				if stat, _ := os.Stdin.Stat(); (stat.Mode() & os.ModeCharDevice) == 0 {
					b, err := readAll(os.Stdin)
					if err != nil {
						return err
					}
					if t := strings.TrimSpace(b); t != "" {
						return loginWithPastedToken(ctx, apiURL, t)
					}
				}
				return runDeviceFlow(ctx, deviceFlowOpts{
					AuthURL:   authURL,
					APIURL:    apiURL,
					ClientID:  clientID,
					Scopes:    scopes,
					OrgID:     strings.TrimSpace(orgID),
					OrgIDSet:  personal || strings.TrimSpace(orgID) != "",
					NoBrowser: noBrowser,
				})
			}
			if err := login(); err != nil {
				return err
			}
			// Every login variant ends by wiring git for Drive (best-effort,
			// never fatal) so `git clone https://drive.latere.ai/...` is a
			// one-step story after sign-in.
			if !noGit {
				configureDriveGitAfterLogin(ctx, cmd.ErrOrStderr())
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&token, "token", "", "skip device flow; store an access token directly")
	f.StringVar(&apiURL, "api-url", "", "override Cella API base URL (default https://cella.latere.ai)")
	f.StringVar(&authURL, "auth-url", "", "override auth base URL (default https://auth.latere.ai)")
	f.StringVar(&clientID, "client-id", "latere-cli", "OAuth client_id used for the device-code request")
	f.StringVar(&scopes, "scopes", api.LoginScopes,
		"space-delimited scope list")
	f.BoolVar(&personal, "personal", false, "issue the CLI token for personal cellas")
	f.StringVar(&orgID, "org-id", "", "issue the CLI token for this organization id")
	f.BoolVar(&noBrowser, "no-browser", false, "print the device URL without opening a browser")
	f.BoolVar(&noGit, "no-git", false, "do not configure git's credential helper for drive.latere.ai")
	return cmd
}

func loginWithPastedToken(ctx context.Context, apiURL, token string) error {
	if err := saveAndVerify(ctx, apiURL, token); err != nil {
		return err
	}
	clearStaleAuthToken()
	return nil
}

// clearStaleAuthToken removes any retained auth.latere.ai root token after a
// successful --token / stdin paste login. A pasted opaque token carries no refresh grant,
// so a leftover auth-token.json from a prior login is never the right identity:
// `latere lux` reads it via api.LoadAuthToken and would silently attribute cost
// to the wrong principal. Clearing it makes lux fall back to the truthful
// not-signed-in state. Best-effort: a failure must not block a valid Cella
// login, so it is warned and ignored.
func clearStaleAuthToken() {
	if err := api.ClearAuthToken(); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not clear stale auth token for lux (%v); `latere lux` may use a previous identity\n", err)
	}
}

// saveAndVerify confirms the candidate by listing sandboxes before storing it.
// Shared by the --token fast path and the device-code happy path.
func saveAndVerify(ctx context.Context, apiURL, token string) error {
	c := api.NewClient(apiURL)
	c.Token = token
	// A refresh would verify a different bearer, potentially from the previous
	// identity, and persist it even though the submitted token was rejected.
	c.Refresh = nil
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var ignored any
	if err := c.GetJSON(verifyCtx, "/v1/sandboxes", &ignored); err != nil {
		return fmt.Errorf("token rejected by Cella API: %w", err)
	}
	if err := api.SaveToken("", api.Token{
		AccessToken: token,
		TokenType:   "Bearer",
		IssuedAt:    time.Now().UTC(),
	}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged in. Token saved to %s\n", api.TokenPath())
	return nil
}

// ---- device-code flow ----

type deviceFlowOpts struct {
	AuthURL, APIURL, ClientID, Scopes string
	OrgID                             string
	OrgIDSet                          bool
	NoBrowser                         bool
}

// Resolve the same configured endpoints for device authorization, token
// exchange, and verification. Explicit flags override environment defaults.
func (opts deviceFlowOpts) endpoints() (authBase, apiBase string) {
	apiBase = api.NewClient(opts.APIURL).BaseURL
	return resolveAuthURL(apiBase, opts.AuthURL), apiBase
}

func resolveAuthURL(apiBase, authBase string) string {
	if authBase == "" {
		authBase = os.Getenv("AUTH_URL")
	}
	if authBase == "" {
		authBase = api.InferAuthURL(apiBase)
	}
	return strings.TrimRight(authBase, "/")
}

// captureStore holds the device-flow candidate in memory until Cella exchange,
// verification, and token storage succeed. A rejected login must not replace
// the auth identity used by other commands while retaining the old Cella token.
type captureStore struct {
	disk *authkit.FileTokenStore
	last *oauth2.Token
}

func newAuthTokenStore() (*captureStore, error) {
	p := api.AuthTokenPath()
	if p == "" {
		return nil, errors.New("cannot determine auth token path")
	}
	disk, err := authkit.NewFileTokenStore(p)
	if err != nil {
		return nil, err
	}
	return &captureStore{disk: disk}, nil
}

func (s *captureStore) Save(t *oauth2.Token) error {
	if t == nil {
		return errors.New("nil token")
	}
	s.last = t
	return nil
}

// persist retains the verified login's root token for refresh and Lux access.
// The caller must finish saving the Cella credential before invoking it.
func (s *captureStore) persist() {
	t := s.last
	// Persist in the api.Token shape so `latere lux` (which reads via
	// api.LoadAuthToken) finds the auth-issued root token where it
	// expects it. Best-effort: lux access is additive to the Cella login.
	if err := api.SaveAuthToken(api.Token{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    t.Expiry,
		IssuedAt:     time.Now().UTC(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not save auth token for lux (%v); `latere lux` may require re-login\n", err)
	}
}

func (s *captureStore) Load() (*oauth2.Token, error) { return s.disk.Load() }
func (s *captureStore) Clear() error                 { return s.disk.Clear() }

// runDeviceFlow drives the RFC 8628 device-code flow against
// auth.latere.ai via pkg/authkit.DeviceCodeClient, then trades the
// resulting auth-issued token for a Cella-scoped one.
func runDeviceFlow(ctx context.Context, opts deviceFlowOpts) error {
	opts.AuthURL, opts.APIURL = opts.endpoints()

	client := oidc.New(oidc.Config{
		AuthURL:  opts.AuthURL,
		ClientID: opts.ClientID,
		Scopes:   strings.Fields(opts.Scopes),
	})
	if client == nil {
		return errors.New("oidc: missing AuthURL or ClientID")
	}

	store, err := newAuthTokenStore()
	if err != nil {
		return err
	}

	extra := url.Values{}
	if opts.OrgIDSet {
		// Forward the present-but-possibly-empty value. Auth reads
		// `?org_id=` (explicit empty) as "personal context" — silently
		// dropping it would turn --personal into a no-op.
		extra["org_id"] = []string{opts.OrgID}
	}

	dcc := authkit.NewDeviceCodeClient(client, store)
	dcc.Output = os.Stderr
	dcc.ExtraParams = extra
	if opts.NoBrowser {
		dcc.OpenBrowser = func(string) error { return nil }
	} else {
		dcc.OpenBrowser = openBrowser
	}

	if err := dcc.Login(ctx); err != nil {
		// Surface terminal RFC 8628 errors with the CLI's user-facing
		// strings; everything else passes through with authkit's wrap.
		if rerr, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
			switch rerr.ErrorCode {
			case "expired_token":
				return errors.New("device code expired before approval")
			case "access_denied":
				return errors.New("user denied the request")
			}
			return fmt.Errorf("device-code login failed: %s (%s)", rerr.ErrorCode, rerr.ErrorDescription)
		}
		return err
	}

	tok := store.last
	if tok == nil || tok.AccessToken == "" {
		return errors.New("token endpoint returned no access_token")
	}

	// Best-effort: trade the auth-issued token for a cella-issued
	// bearer. Falls back to the auth token during the deprecation
	// window so installs without the cella catalog keep working.
	candidate := tok.AccessToken
	if cellaTok, err := exchangeForCellaToken(ctx, opts, tok.AccessToken); err == nil && cellaTok != "" {
		candidate = cellaTok
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "  cella token exchange unavailable (%v); using auth-issued token\n", err)
	}
	if err := saveAndVerify(ctx, opts.APIURL, candidate); err != nil {
		return err
	}
	store.persist()
	return nil
}

var openBrowser = func(rawURL string) error {
	name, args, err := browserCommand(rawURL)
	if err != nil {
		return err
	}
	return exec.Command(name, args...).Start()
}

func browserCommand(rawURL string) (string, []string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{rawURL}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}, nil
	case "linux":
		return "xdg-open", []string{rawURL}, nil
	default:
		return "", nil, fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}

// exchangeForCellaToken trades an auth-issued user JWT for a cella
// bearer token. The preferred path mints a short-TTL actor token at
// auth, then exchanges it at cella's /v1/tokens/exchange.
//
// Some deployed auth versions stamp device-code tokens with sandboxd's
// audience, then reject those same tokens on /actor-tokens because the
// auth middleware expects the auth issuer as audience. In that case the
// device token is still accepted by sandboxd, so use it directly for the
// cella exchange instead of persisting the short-lived auth token.
func exchangeForCellaToken(ctx context.Context, opts deviceFlowOpts, authToken string) (string, error) {
	authBase, apiBase := opts.endpoints()

	httpc := &http.Client{Timeout: 15 * time.Second, Transport: otel.Transport(nil)}

	// 1. Mint an actor token at auth.
	// Sandboxd validates auth-issued actor tokens against SANDBOXD_AUDIENCE.
	actorToken, err := api.MintActorToken(ctx, httpc, authBase, authToken, "sandboxd", 60)
	if err != nil {
		// Auth degrades device tokens to the sandboxd-only audience when
		// the client's allowed_audiences lookup fails; /actor-tokens then
		// rejects them (audience mismatch). The device token is still
		// accepted by sandboxd, so fall back to exchanging it directly.
		if errors.Is(err, api.ErrActorAudienceMismatch) {
			return api.ExchangeAtCella(ctx, httpc, apiBase, authToken)
		}
		return "", err
	}

	// 2. Exchange the actor token at cella.
	return api.ExchangeAtCella(ctx, httpc, apiBase, actorToken)
}

func newAuthWhoamiCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Print the current principal.",
		Long: `Print the principal and token context currently used by the CLI.

For auth-issued tokens this asks auth.latere.ai for token information.
For Cella-issued tokens, it first confirms the token is accepted by
Cella, then prints the identity claims embedded in the saved JWT.`,
		Example: `  latere whoami
  latere whoami --api-url https://cella.latere.ai`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := api.NewClient(apiURL)
			if err := c.MustRequireAuth(); err != nil {
				return err
			}
			// Use the same configured auth endpoint as login; only infer an
			// issuer from the Cella URL when no AUTH_URL override is present.
			authURL := resolveAuthURL(c.BaseURL, "")
			req := *c
			req.BaseURL = authURL
			// The probe 401s by design for cella-issued tokens; a
			// bearer refresh cannot change that outcome.
			req.Refresh = nil
			var info struct {
				Sub           string   `json:"sub"`
				Email         *string  `json:"email,omitempty"`
				PrincipalType string   `json:"principal_type"`
				OrgID         *string  `json:"org_id,omitempty"`
				Scopes        []string `json:"scopes"`
				ClientID      string   `json:"client_id,omitempty"`
			}
			if err := req.GetJSON(cmd.Context(), "/tokeninfo", &info); err == nil {
				printPrincipal(principalInfo{
					Sub:           info.Sub,
					Email:         deref(info.Email),
					PrincipalType: info.PrincipalType,
					OrgID:         deref(info.OrgID),
					Scopes:        info.Scopes,
					ClientID:      info.ClientID,
				})
				return nil
			}
			// /tokeninfo is best-effort: it 401s on cella- and sandbox-issued
			// tokens, and is unreachable when the inferred auth host does not
			// resolve (custom or local --api-url). On any failure, fall back to
			// verifying the bearer against sandboxd and printing the JWT claims.

			// Auth cannot introspect cella-issued tokens, and current
			// auth deployments also reject sandbox-audience device
			// tokens on /tokeninfo. Confirm sandboxd accepts the bearer,
			// then print the identity claims embedded in the JWT.
			var ignored any
			if err := c.GetJSON(cmd.Context(), "/v1/sandboxes", &ignored); err != nil {
				return err
			}
			local, err := principalFromJWT(c.Token)
			if err != nil {
				return err
			}
			printPrincipal(local)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL")
	return cmd
}

type principalInfo struct {
	Sub           string
	Email         string
	PrincipalType string
	OrgID         string
	Scopes        []string
	ClientID      string
}

func printPrincipal(info principalInfo) {
	fmt.Printf("sub:           %s\n", info.Sub)
	if info.Email != "" {
		fmt.Printf("email:         %s\n", info.Email)
	}
	fmt.Printf("principal:     %s\n", info.PrincipalType)
	if info.OrgID != "" {
		fmt.Printf("context:       org\n")
		fmt.Printf("org_id:        %s\n", info.OrgID)
	} else {
		fmt.Printf("context:       personal\n")
	}
	if info.ClientID != "" {
		fmt.Printf("client_id:     %s\n", info.ClientID)
	}
	if len(info.Scopes) > 0 {
		fmt.Printf("scopes:        %s\n", strings.Join(info.Scopes, " "))
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func principalFromJWT(raw string) (principalInfo, error) {
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return principalInfo{}, errors.New("saved token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return principalInfo{}, fmt.Errorf("decode token payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return principalInfo{}, fmt.Errorf("parse token payload: %w", err)
	}
	info := principalInfo{
		Sub:           stringClaim(claims, "sub"),
		Email:         stringClaim(claims, "email"),
		PrincipalType: stringClaim(claims, "principal_type"),
		OrgID:         stringClaim(claims, "org_id"),
		Scopes:        scopesClaim(claims),
		ClientID:      stringClaim(claims, "client_id"),
	}
	if info.Sub == "" {
		return principalInfo{}, errors.New("saved token is missing sub")
	}
	if info.PrincipalType == "" {
		info.PrincipalType = "user"
	}
	return info, nil
}

func stringClaim(claims map[string]any, key string) string {
	v, _ := claims[key].(string)
	return v
}

func scopesClaim(claims map[string]any) []string {
	if scope, _ := claims["scope"].(string); scope != "" {
		return strings.Fields(scope)
	}
	raw, ok := claims["scp"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case string:
		return strings.Fields(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func newAuthLogoutCmd() *cobra.Command {
	var apiURL, authURL string
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Sign out: revoke the session server-side and clear local tokens.",
		Long: `Sign out of Latere.

Revokes the saved cella token server-side (DELETE /v1/tokens/current)
and the retained auth refresh token (RFC 7009 /revoke), then clears
~/.config/latere/token.json and auth-token.json. Server-side revocation
is best-effort: an unreachable or older server prints a warning and the
local sign-out still completes.`,
		Example: `  latere logout
  latere login`,
		RunE: func(cmd *cobra.Command, args []string) error {
			revokeCellaTokenServerSide(cmd.Context(), apiURL, cmd.ErrOrStderr())
			revokeAuthRefreshToken(cmd.Context(), apiURL, authURL, cmd.ErrOrStderr())
			if err := api.ClearToken(""); err != nil {
				return err
			}
			// Also drop the retained auth root token used for lux.
			if err := api.ClearAuthToken(); err != nil {
				return err
			}
			fprintln(cmd.ErrOrStderr(), "Logged out.")
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override Cella API base URL (default https://cella.latere.ai)")
	cmd.Flags().StringVar(&authURL, "auth-url", "", "override auth base URL (default derived from the API URL)")
	return cmd
}

// revokeCellaTokenServerSide best-effort revokes the saved cella
// catalog token via DELETE /v1/tokens/current so it dies now instead
// of at TTL expiry. A sandbox-kind bearer (403), an older cella
// without the endpoint (404), or an unreachable server degrades to a
// stderr note; the local sign-out proceeds regardless.
func revokeCellaTokenServerSide(ctx context.Context, apiURL string, errw io.Writer) {
	c := api.NewClient(apiURL)
	c.Refresh = nil // never mint a fresh credential just to revoke it
	if c.Token == "" {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := c.Do(rctx, http.MethodDelete, "/v1/tokens/current", nil, "", nil)
	if err == nil {
		return
	}
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusForbidden) {
		fprintf(errw, "  note: server-side token revocation unavailable (%d); the token expires on its own\n", apiErr.Status)
		return
	}
	fprintf(errw, "  warning: could not revoke the cella token server-side (%v); it remains valid until expiry\n", err)
}

// revokeAuthRefreshToken best-effort revokes the retained auth refresh
// token via RFC 7009 (POST {auth}/revoke, public client) so the root
// credential cannot mint further access tokens after sign-out.
func revokeAuthRefreshToken(ctx context.Context, apiURL, authURL string, errw io.Writer) {
	tok, err := api.LoadAuthToken()
	if err != nil || tok.RefreshToken == "" {
		return
	}
	authBase := strings.TrimRight(authURL, "/")
	if authBase == "" {
		authBase = strings.TrimRight(os.Getenv("AUTH_URL"), "/")
	}
	if authBase == "" {
		authBase = api.InferAuthURL(api.NewClient(apiURL).BaseURL)
	}
	cid := os.Getenv("AUTH_CLIENT_ID")
	if cid == "" {
		cid = "latere-cli"
	}
	form := url.Values{
		"token":           {tok.RefreshToken},
		"token_type_hint": {"refresh_token"},
		"client_id":       {cid},
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, authBase+"/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		fprintf(errw, "  warning: could not revoke the auth refresh token (%v)\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 10 * time.Second, Transport: otel.Transport(nil)}).Do(req)
	if err != nil {
		fprintf(errw, "  warning: could not revoke the auth refresh token (%v); it remains valid until expiry\n", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		fprintf(errw, "  warning: auth refresh-token revocation returned %d; it may remain valid until expiry\n", resp.StatusCode)
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
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
	}
	return string(buf), nil
}
