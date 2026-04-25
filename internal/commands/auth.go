package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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
	cmd.AddCommand(newAuthLogoutCmd())
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	var (
		token   string
		apiURL  string
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store a sandboxd access token in ~/.config/latere/token.json.",
		Long: `Store an access token used by every other latere command.

Until the device-code flow on auth.latere.ai ships, login takes a token
either via --token or piped on stdin. Get one from the dashboard at
https://auth.latere.ai/me/token. The token is written to
~/.config/latere/token.json with 0600 perms; the MCP server
(sandbox-mcp) reads the same file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			t := strings.TrimSpace(token)
			if t == "" {
				// Stdin fallback: piped or interactive paste.
				stat, _ := os.Stdin.Stat()
				if (stat.Mode() & os.ModeCharDevice) == 0 {
					b, err := readAll(os.Stdin)
					if err != nil {
						return err
					}
					t = strings.TrimSpace(b)
				} else {
					fmt.Fprint(os.Stderr, "Paste access token: ")
					line, err := bufio.NewReader(os.Stdin).ReadString('\n')
					if err != nil {
						return err
					}
					t = strings.TrimSpace(line)
				}
			}
			if t == "" {
				return errors.New("no token provided")
			}
			if err := api.SaveToken("", api.Token{
				AccessToken: t,
				TokenType:   "Bearer",
				IssuedAt:    time.Now().UTC(),
			}); err != nil {
				return err
			}
			// Verify by hitting /v1/sandboxes (it's behind auth and
			// cheap; a 200/empty list confirms the token).
			c := api.NewClient(apiURL)
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			var ignored any
			if err := c.GetJSON(ctx, "/v1/sandboxes", &ignored); err != nil {
				_ = api.ClearToken("")
				return fmt.Errorf("token rejected by sandboxd: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Logged in. Token saved to %s\n", api.TokenPath())
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "access token (skips stdin prompt)")
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override sandboxd base URL (default https://cella.latere.ai)")
	return cmd
}

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
