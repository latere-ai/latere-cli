package commands

import (
	"archive/tar"
	"archive/zip"
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
	"path/filepath"
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

type oneShotRunDTO struct {
	RunID       string `json:"run_id"`
	SandboxID   string `json:"sandbox_id"`
	SandboxName string `json:"sandbox_name"`
	State       string `json:"state"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Timing      struct {
		CreateMS  int64 `json:"create_ms"`
		ExecMS    int64 `json:"exec_ms"`
		CleanupMS int64 `json:"cleanup_ms"`
		TotalMS   int64 `json:"total_ms"`
	} `json:"timing"`
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	Truncated    bool   `json:"truncated"`
	Error        string `json:"error,omitempty"`
	CleanupError string `json:"cleanup_error,omitempty"`
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
		newCeExecCmd(),
		newCeRunCmd(),
		newCeLogsCmd(),
		newCeWaitCmd(),
		newCeImportCmd(),
		newCeExportCmd(),
		newCeExtendCmd(),
		newCeConvertCmd(),
		newCeMcpCmd(),
	)
	return cmd
}

// newExecCmd is the top-level `latere exec` shortcut. The same
// behavior is also wired in under `latere cella exec` via
// newCeExecCmd, so both forms work.
func newExecCmd() *cobra.Command {
	cmd := newCeExecCmd()
	cmd.Use = "exec <name|id> -- <cmd>..."
	return cmd
}

// newCeExecCmd registers `latere cella exec`, the synchronous
// streaming variant of `cella run`.
func newCeExecCmd() *cobra.Command {
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
		apiURL       string
		envFlag      []string
		cwd          string
		follow       bool
		ephemeral    bool
		rm           bool
		image        string
		diskGB       int
		timeout      int
		printJSONOut bool
	)
	cmd := &cobra.Command{
		Use:   "run [name|id] -- <argv>...",
		Short: "Run a command in a cella, or one-shot in a disposable ephemeral cella.",
		Args: func(cmd *cobra.Command, args []string) error {
			if ephemeral || rm {
				if !ephemeral || !rm {
					return fmt.Errorf("--ephemeral and --rm must be used together for one-shot runs")
				}
				if len(args) == 0 {
					return fmt.Errorf("missing argv after --")
				}
				return nil
			}
			if len(args) < 2 {
				return fmt.Errorf("requires <name|id> -- <argv>... unless --ephemeral --rm is set")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			env, err := parseKV(envFlag)
			if err != nil {
				return err
			}
			if ephemeral && rm {
				out, err := oneShotRun(cmd.Context(), c, args, env, cwd, image, diskGB, timeout)
				if err != nil {
					return err
				}
				if printJSONOut {
					return printJSON(out)
				}
				printOneShotRun(out)
				if out.ExitCode != nil {
					os.Exit(*out.ExitCode)
				}
				return nil
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
	f.BoolVar(&ephemeral, "ephemeral", false, "create a disposable one-shot ephemeral cella for this command")
	f.BoolVar(&rm, "rm", false, "delete the one-shot cella after the command; required with --ephemeral")
	f.StringVar(&image, "image", "", "one-shot image ref (default sandbox-base)")
	f.IntVar(&diskGB, "disk", 0, "one-shot PVC size in GB (default 5)")
	f.IntVar(&timeout, "timeout", 600, "one-shot command timeout in seconds")
	f.BoolVar(&printJSONOut, "json", false, "print one-shot response as JSON")
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
		apiURL  string
		dest    string
		input   string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "import <name|id>",
		Short: "Upload files into the cella workspace (reads stdin or --input).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			if c.HTTP != nil {
				c.HTTP.Timeout = timeout
			}
			var (
				src          io.Reader = os.Stdin
				srcFile      *os.File
				formFilename = "import.tar"
				inputKind    = importInputTar
			)
			if input != "" && input != "-" {
				f, err := os.Open(input)
				if err != nil {
					return err
				}
				defer f.Close()
				src = f
				srcFile = f
				formFilename = filepath.Base(input)
				inputKind, err = classifyImportInput(input, f)
				if err != nil {
					return err
				}
			}
			pr, pw := io.Pipe()
			mw := multipart.NewWriter(pw)
			go func() {
				if dest != "" {
					if err := mw.WriteField("dest", dest); err != nil {
						_ = pw.CloseWithError(err)
						return
					}
				}
				fw, err := mw.CreateFormFile("tarball", formFilename)
				if err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				switch inputKind {
				case importInputRegularFile:
					err = writeSingleFileTar(fw, input, srcFile)
				case importInputZip:
					err = writeZipAsTar(fw, input, srcFile)
				default:
					_, err = io.Copy(fw, src)
				}
				if err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				if err := mw.Close(); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				_ = pw.Close()
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
	f.StringVarP(&input, "input", "i", "-", "input path; tar archives are extracted, regular files are copied")
	f.DurationVar(&timeout, "timeout", 30*time.Minute, "HTTP timeout covering upload and extraction (0 disables)")
	return cmd
}

type importInputKind int

const (
	importInputTar importInputKind = iota
	importInputRegularFile
	importInputZip
)

func classifyImportInput(name string, f *os.File) (importInputKind, error) {
	info, err := f.Stat()
	if err != nil {
		return importInputTar, err
	}
	if info.IsDir() {
		return importInputTar, fmt.Errorf("input must be a file, got directory: %s", name)
	}
	if hasZipExtension(name) {
		return importInputZip, nil
	}
	if hasTarExtension(name) {
		return importInputTar, nil
	}
	kind, err := sniffImportInput(f)
	if err != nil {
		return importInputTar, err
	}
	return kind, nil
}

func hasTarExtension(name string) bool {
	name = strings.ToLower(name)
	for _, suffix := range []string{".tar", ".tar.gz", ".tgz", ".tar.bz2", ".tbz", ".tbz2", ".tar.xz", ".txz"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func hasZipExtension(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".zip")
}

func sniffImportInput(f *os.File) (importInputKind, error) {
	var block [512]byte
	n, err := io.ReadFull(f, block[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return importInputTar, err
	}
	if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
		return importInputTar, seekErr
	}
	if n >= 4 && string(block[:2]) == "PK" &&
		(block[2] == 0x03 || block[2] == 0x05 || block[2] == 0x07) &&
		(block[3] == 0x04 || block[3] == 0x06 || block[3] == 0x08) {
		return importInputZip, nil
	}
	if n < len(block) {
		return importInputRegularFile, nil
	}
	if string(block[257:262]) == "ustar" {
		return importInputTar, nil
	}
	return importInputRegularFile, nil
}

func writeSingleFileTar(dst io.Writer, name string, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tw := tar.NewWriter(dst)
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.Base(name)
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := io.Copy(tw, f); err != nil {
		return err
	}
	return tw.Close()
}

func writeZipAsTar(dst io.Writer, name string, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return fmt.Errorf("read zip %s: %w", name, err)
	}
	tw := tar.NewWriter(dst)
	for _, zf := range zr.File {
		if !safeArchivePath(zf.Name) {
			return fmt.Errorf("zip entry has unsafe path: %s", zf.Name)
		}
		hdr, err := tar.FileInfoHeader(zf.FileInfo(), "")
		if err != nil {
			return err
		}
		hdr.Name = strings.TrimPrefix(zf.Name, "./")
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, rc)
		closeErr := rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return tw.Close()
}

func safeArchivePath(name string) bool {
	name = strings.TrimPrefix(name, "./")
	return name != "" && !filepath.IsAbs(name) && !strings.Contains(name, "\x00") &&
		name != "." && !strings.HasPrefix(name, "../") && !strings.Contains(name, "/../")
}

// ---- extend / convert ----

// newCeExtendCmd pushes the auto-delete deadline of an ephemeral
// cella forward. Persistent cellas have no deadline so the API 409s.
func newCeExtendCmd() *cobra.Command {
	var (
		apiURL   string
		hours    int
		deadline string
	)
	cmd := &cobra.Command{
		Use:   "extend <name|id>",
		Short: "Push the auto-delete deadline of an ephemeral cella forward.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			body := map[string]any{}
			if deadline != "" {
				t, err := time.Parse(time.RFC3339, deadline)
				if err != nil {
					return fmt.Errorf("--deadline must be RFC3339: %w", err)
				}
				body["deadline"] = t
			} else {
				if hours <= 0 {
					hours = 24
				}
				body["auto_delete_hours"] = hours
			}
			b, _ := json.Marshal(body)
			var sb sandboxDTO
			path := sbPath(args[0]) + "/extend"
			if err := c.Do(cmd.Context(), http.MethodPost, path,
				bytes.NewReader(b), "application/json", &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	f.IntVar(&hours, "hours", 24, "push deadline to now + N hours")
	f.StringVar(&deadline, "deadline", "", "absolute RFC3339 deadline (overrides --hours)")
	return cmd
}

// newCeConvertCmd flips a cella between ephemeral and persistent.
// Persistent → ephemeral requires --hours so the new lifetime is
// explicit; the API rejects the request otherwise.
func newCeConvertCmd() *cobra.Command {
	var (
		apiURL string
		to     string
		hours  int
	)
	cmd := &cobra.Command{
		Use:   "convert <name|id> --to {ephemeral|persistent}",
		Short: "Switch a cella between ephemeral and persistent.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if to != "ephemeral" && to != "persistent" {
				return fmt.Errorf("--to must be ephemeral or persistent")
			}
			c, err := authedClient(apiURL)
			if err != nil {
				return err
			}
			body := map[string]any{"tier": to}
			if to == "ephemeral" {
				if hours <= 0 {
					return fmt.Errorf("--hours is required when converting to ephemeral")
				}
				body["auto_delete_hours"] = hours
			}
			b, _ := json.Marshal(body)
			var sb sandboxDTO
			path := sbPath(args[0]) + "/convert"
			if err := c.Do(cmd.Context(), http.MethodPost, path,
				bytes.NewReader(b), "application/json", &sb); err != nil {
				return err
			}
			printSandbox(sb)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", "override sandboxd base URL")
	f.StringVar(&to, "to", "", "destination tier: ephemeral or persistent")
	f.IntVar(&hours, "hours", 0, "auto-delete-hours; required when --to=ephemeral")
	_ = cmd.MarkFlagRequired("to")
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
// SSE follow mode is the alternative; cursor polling works against
// a simpler sandboxd build and survives reconnects naturally.
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

func oneShotRun(ctx context.Context, c *api.Client, argv []string, env map[string]string, cwd, image string, diskGB, timeout int) (oneShotRunDTO, error) {
	body := map[string]any{"argv": argv}
	if len(env) > 0 {
		body["env"] = env
	}
	if cwd != "" {
		body["cwd"] = cwd
	}
	if image != "" {
		body["image"] = image
	}
	if diskGB > 0 {
		body["disk_gb"] = diskGB
	}
	if timeout > 0 {
		body["timeout_seconds"] = timeout
	}
	if c.HTTP != nil {
		effective := timeout
		if effective <= 0 {
			effective = 600
		}
		c.HTTP.Timeout = time.Duration(effective+180) * time.Second
	}
	var out oneShotRunDTO
	err := c.PostJSON(ctx, "/v1/one-shot-runs", body, &out)
	return out, err
}

func printOneShotRun(out oneShotRunDTO) {
	if out.Stdout != "" {
		fmt.Print(out.Stdout)
	}
	if out.Stderr != "" {
		fmt.Fprint(os.Stderr, out.Stderr)
	}
	fmt.Fprintf(os.Stderr, "✓ sandbox created  %s  ·  %s\n", out.SandboxName, humanDurationMS(out.Timing.CreateMS))
	if out.ExitCode != nil {
		fmt.Fprintf(os.Stderr, "✓ command exited %d  ·  %s\n", *out.ExitCode, humanDurationMS(out.Timing.ExecMS))
	} else {
		fmt.Fprintf(os.Stderr, "✓ command %s  ·  %s\n", out.State, humanDurationMS(out.Timing.ExecMS))
	}
	if out.CleanupError != "" {
		fmt.Fprintf(os.Stderr, "✗ sandbox cleanup failed  ·  %s\n", out.CleanupError)
	} else {
		fmt.Fprintf(os.Stderr, "✓ sandbox deleted  ·  total %s\n", humanDurationMS(out.Timing.TotalMS))
	}
	if out.Truncated {
		fmt.Fprintln(os.Stderr, "output truncated")
	}
	if out.Error != "" {
		fmt.Fprintf(os.Stderr, "%s\n", out.Error)
	}
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

func humanDurationMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	if ms < 10_000 {
		return fmt.Sprintf("%.1f s", float64(ms)/1000)
	}
	return fmt.Sprintf("%d s", ms/1000)
}
