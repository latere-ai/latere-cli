package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
)

// ---- DTOs (subset of sandboxd's OpenAPI; keep loose so additive
//      backend changes don't break the CLI). ----

type sandboxDTO struct {
	ID              string            `json:"id"`
	Name            string            `json:"name,omitempty"`
	State           string            `json:"state"`
	Tier            string            `json:"tier,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	LastActivityAt  time.Time         `json:"last_activity_at,omitzero"`
	AutoStopMinutes int               `json:"auto_stop_minutes,omitempty"`
	DiskGB          int               `json:"disk_gb,omitempty"`
	Deadline        time.Time         `json:"deadline,omitzero"`
	Annotations     map[string]string `json:"annotations,omitempty"`
}

type commandDTO struct {
	CommandID string    `json:"command_id"`
	Phase     string    `json:"phase"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	StartedAt time.Time `json:"started_at"`
	ExitedAt  time.Time `json:"exited_at,omitzero"`
}

type logsCursorDTO struct {
	Bytes      string `json:"bytes"`
	NextCursor int64  `json:"next_cursor"`
	Phase      string `json:"phase"`
	ExitCode   *int   `json:"exit_code,omitempty"`
}

// ---- top-level ----

// newCellaCmd is the canonical `latere cella …` command tree. The
// underlying API resource is "sandbox", but the product brand — and
// the matching surface on https://latere.ai/cella — is Cella, so the
// CLI follows the brand. `latere sandbox …` stays as a hidden alias
// for v0.1.0 compatibility.
func newCellaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cella",
		Aliases: []string{"sandbox"},
		Short:   "Manage cellas (create, list, rename, start, stop, delete, run).",
		Long: `Manage Cella sandboxes — per-user compute environments at cella.latere.ai.

Each cella is a PVC-backed workspace plus a Pod for compute. Tier
'ephemeral' auto-stops on idle and auto-deletes after a wall-clock
window; tier 'persistent' stays until you delete it.`,
	}
	cmd.AddCommand(
		newCeCreateCmd(),
		newCeListCmd(),
		newCeGetCmd(),
		newCeRenameCmd(),
		newCeStartCmd(),
		newCeStopCmd(),
		newCeDeleteCmd(),
		newCeRunCmd(),
		newCeLogsCmd(),
		newCeWaitCmd(),
		newCeImportCmd(),
		newCeExportCmd(),
		newCeMcpCmd(),
	)
	return cmd
}

// newExecCmd is the top-level `latere exec` shortcut.
func newExecCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "exec <name|id> -- <cmd>...",
		Short: "Run a command synchronously inside a cella (streams logs to stdout).",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			sandbox := args[0]
			argv := args[1:]
			return runAndStream(cmd.Context(), c, sandbox, argv, nil, "")
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override cella base URL")
	return cmd
}

// ---- create / list / get / rename / start / stop / delete ----

func newCeCreateCmd() *cobra.Command {
	var (
		image           string
		name            string
		tier            string
		diskGB          int
		autoStop        int
		autoDeleteHours int
		ttl             string
		envFlag         []string
		policy          string
		apiURL          string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a cella.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			env, err := parseKV(envFlag)
			if err != nil {
				return err
			}
			body := map[string]any{
				"image": image,
				"name":  name,
			}
			if tier != "" {
				body["tier"] = tier
			}
			if diskGB > 0 {
				body["disk_gb"] = diskGB
			}
			if autoStop >= 0 && cmd.Flags().Changed("auto-stop-minutes") {
				body["auto_stop_minutes"] = autoStop
			}
			if autoDeleteHours > 0 {
				body["auto_delete_hours"] = autoDeleteHours
			}
			if ttl != "" {
				body["ttl"] = ttl
			}
			if len(env) > 0 {
				body["env"] = env
			}
			if policy != "" {
				body["policy"] = policy
			}
			var sb sandboxDTO
			if err := c.PostJSON(cmd.Context(), "/v1/sandboxes", body, &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&image, "image", "ghcr.io/latere-ai/sandbox-base:main", "container image")
	f.StringVar(&name, "name", "", "human slug; server generates one if omitted")
	f.StringVar(&tier, "tier", "", "ephemeral|persistent (default ephemeral)")
	f.IntVar(&diskGB, "disk", 0, "PVC size in GB (default 5)")
	f.IntVar(&autoStop, "auto-stop-minutes", -1, "idle timeout (default 15; 0 disables)")
	f.IntVar(&autoDeleteHours, "auto-delete-hours", 0, "ephemeral wall-clock lifetime")
	f.StringVar(&ttl, "ttl", "", "Go duration TTL alternative to --auto-delete-hours")
	f.StringArrayVar(&envFlag, "env", nil, "KEY=VALUE; repeatable")
	f.StringVar(&policy, "policy", "", "named NetworkPolicy")
	f.StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	return cmd
}

func newCeListCmd() *cobra.Command {
	var (
		apiURL string
		jsonF  bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your cellas.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			var sbs []sandboxDTO
			if err := c.GetJSON(cmd.Context(), "/v1/sandboxes", &sbs); err != nil {
				return err
			}
			if jsonF {
				return printJSON(sbs)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tID\tSTATE\tTIER\tDISK\tCREATED")
			for _, s := range sbs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%dGi\t%s\n",
					nameOrDash(s.Name), s.ID, s.State, defaultStr(s.Tier, "-"),
					s.DiskGB, humanAge(s.CreatedAt))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func newCeGetCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "get <name|id>",
		Short: "Get a cella by name or id.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			var sb sandboxDTO
			if err := c.GetJSON(cmd.Context(), sbPath(args[0]), &sb); err != nil {
				return err
			}
			return printJSON(sb)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	return cmd
}

func newCeRenameCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "rename <name|id> <new-name>",
		Short: "Rename a cella.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			var sb sandboxDTO
			body := map[string]any{"name": args[1]}
			if err := c.Do(cmd.Context(), http.MethodPatch, sbPath(args[0]),
				jsonReader(body), "application/json", &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	return cmd
}

func newCeStartCmd() *cobra.Command { return simpleAction("start", "Start a stopped sandbox.") }
func newCeStopCmd() *cobra.Command  { return simpleAction("stop", "Stop a running sandbox.") }

func simpleAction(verb, short string) *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   verb + " <name|id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			var sb sandboxDTO
			path := sbPath(args[0]) + "/" + verb
			if err := c.Do(cmd.Context(), http.MethodPost, path, nil, "", &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	return cmd
}

func newCeDeleteCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a cella (workspace contents are lost).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			if err := c.Do(cmd.Context(), http.MethodDelete, sbPath(args[0]), nil, "", nil); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "deleted %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	return cmd
}

// ---- run / logs / wait ----

func newCeRunCmd() *cobra.Command {
	var (
		apiURL  string
		envFlag []string
		cwd     string
		follow  bool
	)
	cmd := &cobra.Command{
		Use:   "run <name|id> -- <argv>...",
		Short: "Start a command in the background; prints command_id (or follows logs with -f).",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			env, err := parseKV(envFlag)
			if err != nil {
				return err
			}
			if follow {
				return runAndStream(cmd.Context(), c, args[0], args[1:], env, cwd)
			}
			cd, err := startCommand(cmd.Context(), c, args[0], args[1:], env, cwd)
			if err != nil {
				return err
			}
			fmt.Println(cd.CommandID)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	f.StringArrayVar(&envFlag, "env", nil, "KEY=VALUE; repeatable")
	f.StringVar(&cwd, "cwd", "", "working dir inside the cella")
	f.BoolVarP(&follow, "follow", "f", false, "stream logs and exit with the command's exit code")
	return cmd
}

func newCeLogsCmd() *cobra.Command {
	var (
		apiURL string
		cursor int64
		follow bool
	)
	cmd := &cobra.Command{
		Use:   "logs <name|id> <command_id>",
		Short: "Read or follow command logs.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			if follow {
				return streamLogs(cmd.Context(), c, args[0], args[1], cursor)
			}
			out, err := fetchLogsCursor(cmd.Context(), c, args[0], args[1], cursor)
			if err != nil {
				return err
			}
			fmt.Print(out.Bytes)
			fmt.Fprintf(os.Stderr, "[cursor=%d phase=%s]\n", out.NextCursor, out.Phase)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	f.Int64Var(&cursor, "cursor", 0, "byte offset to start from")
	f.BoolVarP(&follow, "follow", "f", false, "stream until command exits")
	return cmd
}

func newCeWaitCmd() *cobra.Command {
	var (
		apiURL string
		secs   int
	)
	cmd := &cobra.Command{
		Use:   "wait <name|id> <command_id>",
		Short: "Poll a command until it terminates or --timeout passes.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			cd, err := waitCommand(cmd.Context(), c, args[0], args[1], time.Duration(secs)*time.Second)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "phase=%s", cd.Phase)
			if cd.ExitCode != nil {
				fmt.Fprintf(os.Stderr, " exit_code=%d", *cd.ExitCode)
				os.Exit(*cd.ExitCode)
			}
			fmt.Fprintln(os.Stderr)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	cmd.Flags().IntVar(&secs, "timeout", 600, "max poll seconds")
	return cmd
}

// ---- import / export ----

func newCeExportCmd() *cobra.Command {
	var (
		apiURL string
		srcDir string
		out    string
	)
	cmd := &cobra.Command{
		Use:   "export <name|id> [paths...]",
		Short: "Stream a tar of files from the cella workspace.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			body := map[string]any{}
			if srcDir != "" {
				body["src_dir"] = srcDir
			}
			if len(args) > 1 {
				body["paths"] = args[1:]
			}
			path := sbPath(args[0]) + "/files/export"
			b, _ := json.Marshal(body)
			resp, err := c.DoRaw(cmd.Context(), http.MethodPost, path,
				bytes.NewReader(b), "application/json")
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			var w io.Writer = os.Stdout
			if out != "" && out != "-" {
				f, err := os.Create(out)
				if err != nil {
					return err
				}
				defer f.Close()
				w = f
			}
			_, err = io.Copy(w, resp.Body)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	f.StringVar(&srcDir, "src-dir", "", "directory inside the cella; default /workspace")
	f.StringVarP(&out, "output", "o", "-", "output tar path (- for stdout)")
	return cmd
}

func newCeImportCmd() *cobra.Command {
	var (
		apiURL string
		dest   string
		input  string
	)
	cmd := &cobra.Command{
		Use:   "import <name|id>",
		Short: "Upload a tar into the cella workspace (reads stdin or --input).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			var src io.Reader = os.Stdin
			if input != "" && input != "-" {
				f, err := os.Open(input)
				if err != nil {
					return err
				}
				defer f.Close()
				src = f
			}
			pr, pw := io.Pipe()
			mw := multipart.NewWriter(pw)
			go func() {
				defer pw.Close()
				if dest != "" {
					_ = mw.WriteField("dest", dest)
				}
				fw, err := mw.CreateFormFile("tarball", "import.tar")
				if err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				if _, err := io.Copy(fw, src); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				_ = mw.Close()
			}()
			path := sbPath(args[0]) + "/files/import"
			var resp struct {
				Imported string `json:"imported"`
				Bytes    int64  `json:"bytes"`
				Dest     string `json:"dest"`
			}
			if err := c.Do(cmd.Context(), http.MethodPost, path, pr,
				mw.FormDataContentType(), &resp); err != nil {
				return err
			}
			return printJSON(resp)
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	f.StringVar(&dest, "dest", "", "destination dir in the sandbox; default /workspace")
	f.StringVarP(&input, "input", "i", "-", "input tar path (- for stdin)")
	return cmd
}

// ---- helpers (HTTP composition + UI) ----

func authedClient(apiURL string) (*api.Client, error) {
	c := api.NewClient(apiURL)
	if err := c.MustRequireAuth(); err != nil {
		return nil, err
	}
	return c, nil
}

func sbPath(idOrName string) string {
	return "/v1/sandboxes/" + url.PathEscape(idOrName)
}

func startCommand(ctx context.Context, c *api.Client, sandbox string, argv []string, env map[string]string, cwd string) (commandDTO, error) {
	body := map[string]any{
		"argv":   argv,
		"detach": true,
	}
	if len(env) > 0 {
		body["env"] = env
	}
	if cwd != "" {
		body["cwd"] = cwd
	}
	var cd commandDTO
	err := c.PostJSON(ctx, sbPath(sandbox)+"/commands", body, &cd)
	return cd, err
}

func waitCommand(ctx context.Context, c *api.Client, sandbox, cmdID string, timeout time.Duration) (commandDTO, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		var cd commandDTO
		path := sbPath(sandbox) + "/commands/" + url.PathEscape(cmdID)
		if err := c.GetJSON(ctx, path, &cd); err != nil {
			return cd, err
		}
		if cd.Phase != "running" {
			return cd, nil
		}
		if time.Now().After(deadline) {
			return cd, errors.New("wait timed out")
		}
		select {
		case <-ctx.Done():
			return cd, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func fetchLogsCursor(ctx context.Context, c *api.Client, sandbox, cmdID string, cursor int64) (logsCursorDTO, error) {
	q := url.Values{}
	q.Set("cursor", strconv.FormatInt(cursor, 10))
	q.Set("stream", "false")
	path := sbPath(sandbox) + "/commands/" + url.PathEscape(cmdID) + "/logs?" + q.Encode()
	var out logsCursorDTO
	err := c.GetJSON(ctx, path, &out)
	return out, err
}

// streamLogs polls cursor-based logs until the command terminates.
// Spec 15's SSE follow mode is the alternative; cursor polling works
// with a simpler sandboxd build and survives reconnects naturally.
func streamLogs(ctx context.Context, c *api.Client, sandbox, cmdID string, cursor int64) error {
	for {
		out, err := fetchLogsCursor(ctx, c, sandbox, cmdID, cursor)
		if err != nil {
			return err
		}
		if out.Bytes != "" {
			fmt.Print(out.Bytes)
		}
		cursor = out.NextCursor
		if out.Phase != "running" {
			if out.ExitCode != nil {
				os.Exit(*out.ExitCode)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// runAndStream is the foreground equivalent: start a detached command
// then tail its logs until exit. Used by `latere exec` and
// `latere sandbox run --follow`.
func runAndStream(ctx context.Context, c *api.Client, sandbox string, argv []string, env map[string]string, cwd string) error {
	cd, err := startCommand(ctx, c, sandbox, argv, env, cwd)
	if err != nil {
		return err
	}
	return streamLogs(ctx, c, sandbox, cd.CommandID, 0)
}

// parseKV turns ["KEY=VALUE", ...] into a map.
func parseKV(items []string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	m := map[string]string{}
	for _, kv := range items {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid env %q (want KEY=VALUE)", kv)
		}
		m[k] = v
	}
	return m, nil
}

func jsonReader(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printSandbox(s sandboxDTO) {
	fmt.Printf("%s  %s  %s\n", nameOrDash(s.Name), s.ID, s.State)
}

func nameOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func humanAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
