package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	adversarial "latere.ai/x/agon/pkg/adversarial"
	"latere.ai/x/agon/pkg/adversarial/claude"
	"latere.ai/x/agon/pkg/adversarial/input"
	atopos "latere.ai/x/agon/pkg/adversarial/topos"
	xtopos "latere.ai/x/topos"
)

// agon runs adversarial review locally on the developer machine. The
// proposer forks the developer's real Claude Code session
// (claude --resume <id> --fork-session, full fidelity), while the critics
// run through topos with their model calls routed via Lux using the
// retained Latere identity bearer, so critic cost is tracked on the
// Latere account and no provider key is needed locally.
//
// See latere-cli/specs/agon-local-subcommand.md and agon
// specs/39-topos-backed-critic.md ("Why critic-only").

// agonOpts holds the resolved flags for one `latere agon` invocation.
type agonOpts struct {
	session   string
	dir       string
	stateDir  string
	forks     int
	maxRounds int
	costCap   int
	model     string
	propTO    time.Duration
	luxURL    string
	authURL   string
	token     string
}

func newAgonCmd() *cobra.Command {
	var o agonOpts
	cmd := &cobra.Command{
		Use:   "agon",
		Short: "Adversarial review of your latest Claude Code session.",
		Long: `Run an adversarial debate over the working-tree diff of your most
recent Claude Code session.

The proposer forks your real Claude Code session
(claude --resume <id> --fork-session) so it argues with the full
transcript, harness context, and working tree. The critics run through
topos with model calls routed via Lux (lux.latere.ai), authenticated by
your retained Latere identity bearer, so critic cost is tracked on your
Latere account with no provider key needed locally.

Run 'latere auth login' first (it grants the llm.invoke scope the
critics need). The proposer additionally needs the 'claude' CLI
installed and authenticated.

By default agon reviews the most recent session under the working
directory; pass --session <id> to pick a specific one.`,
		Example: `  latere agon
  latere agon --forks 3 --max-rounds 6
  latere agon --session 4f3c2b1a --dir ~/code/myrepo`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAgon(cmd.Context(), cmd, &o)
		},
	}
	cmd.Flags().StringVar(&o.session, "session", "", "Claude Code session ID to review (default: most recent under --dir)")
	cmd.Flags().StringVar(&o.dir, "dir", ".", "working directory: git repo root and Claude session home")
	cmd.Flags().StringVar(&o.stateDir, "state-dir", "", "where to write .agon/sessions/ (default: same as --dir)")
	cmd.Flags().IntVar(&o.forks, "forks", 1, "number of independent critic forks")
	cmd.Flags().IntVar(&o.maxRounds, "max-rounds", 4, "per-fork internal-round cap")
	cmd.Flags().IntVar(&o.costCap, "cost-cap", 50000, "soft token budget (proposer tokens; topos critics report no usage yet)")
	cmd.Flags().StringVar(&o.model, "model", "claude-sonnet-4-6", "critic model, routed through Lux")
	cmd.Flags().DurationVar(&o.propTO, "proposer-timeout", 5*time.Minute, "per-round deadline for the proposer's claude call (large sessions may need more)")
	cmd.Flags().StringVar(&o.luxURL, "lux-url", "", "override Lux base URL (overrides LUX_API_URL)")
	cmd.Flags().StringVar(&o.authURL, "auth-url", "", "override auth base URL (default derived from the Lux URL)")
	cmd.Flags().StringVar(&o.token, "token", "", "present this bearer to Lux instead of minting one (e.g. a sandbox token)")
	return cmd
}

func runAgon(ctx context.Context, cmd *cobra.Command, o *agonOpts) error {
	cwd, err := filepath.Abs(o.dir)
	if err != nil {
		return fmt.Errorf("resolve --dir: %w", err)
	}
	stateDir := o.stateDir
	if stateDir == "" {
		stateDir = cwd
	}

	// Resolve the identity bearer up front: validates that the user is
	// signed in for Lux and lets us preflight the llm.invoke scope before
	// spending a proposer round. The same closure is handed to topos so it
	// re-fetches (and refreshes) the bearer on each model call, since a
	// debate can outlive a single short-lived token.
	bearerFn := func(ctx context.Context) (string, error) {
		return luxIdentityBearer(ctx, o.token, o.luxURL, o.authURL)
	}
	bearer, err := bearerFn(ctx)
	if err != nil {
		return err
	}
	if err := ensureLuxScope(bearer, []string{"llm.invoke"}, "run agon critics"); err != nil {
		return err
	}
	if err := ensureBearerFresh(bearer); err != nil {
		return err
	}

	// Locate the session to fork. The proposer needs a real Claude session
	// ID; without one there is nothing to --resume.
	sessionID := o.session
	transcriptPath := ""
	home, herr := os.UserHomeDir()
	if herr != nil {
		return fmt.Errorf("resolve home dir: %w", herr)
	}
	if sessionID == "" {
		id, path, merr := mostRecentSession(home, cwd)
		if merr != nil {
			return merr
		}
		sessionID, transcriptPath = id, path
		fmt.Fprintf(cmd.ErrOrStderr(), "[agon] reviewing most recent session %s\n", sessionID)
	} else {
		path, perr := input.LocateTranscript(home, cwd, sessionID, "")
		if perr != nil {
			return fmt.Errorf("%w (pass --session <id> for a session under a different dir)", perr)
		}
		transcriptPath = path
	}

	// Compute the diff and gate. Mirror cmd/agon's HEAD~1 fallback: when
	// the tree is clean (claude already committed), review the last commit.
	diff, err := input.Compute(ctx, input.DiffSpec{From: "HEAD", To: ".", Cwd: cwd})
	if err != nil {
		return err
	}
	if diff.ChangedLines == 0 {
		if fb, fbErr := input.Compute(ctx, input.DiffSpec{From: "HEAD~1", To: "HEAD", Cwd: cwd}); fbErr == nil && fb.ChangedLines > 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "[agon] working tree clean; falling back to HEAD~1..HEAD")
			diff = fb
		}
	}
	if input.Trivial(diff, 0) {
		fmt.Fprintln(cmd.ErrOrStderr(), "[agon] no substantive diff to review")
		return nil
	}

	// Task context from the session's first user turn (best-effort).
	taskCtx := "(task context unavailable)"
	if tr, rerr := input.ReadTranscript(transcriptPath); rerr == nil && tr.FirstUser != "" {
		taskCtx = tr.FirstUser
	}

	proposer := claude.NewProposer(sessionID, cwd, claude.WithProposerDeadline(o.propTO))
	critics := atopos.NewCriticFactory(atopos.Config{
		Model: xtopos.ModelOptions{
			Kind:         xtopos.ModelLux,
			Provider:     "anthropic",
			Model:        o.model,
			BaseURL:      resolveLuxURL(o.luxURL) + "/anthropic",
			BearerSource: bearerFn,
		},
	})

	summary, err := (&adversarial.Engine{
		StateDir:    stateDir,
		Cwd:         cwd,
		ForkCount:   o.forks,
		Proposer:    proposer,
		NewCritic:   critics,
		MaxRounds:   o.maxRounds,
		CostCap:     o.costCap,
		TaskContext: taskCtx,
		DiffPatch:   diff.Patch,
	}).Run(ctx)
	if err != nil {
		return err
	}
	printAgonSummary(cmd, summary)
	// Verdict in the exit code (matches cmd/agon): a completed debate with
	// open attacks exits non-zero so it can gate CI/hooks, distinct from a
	// command error. main maps this sentinel to exit 2.
	if summary.Unresolved > 0 {
		return &unresolvedError{n: summary.Unresolved}
	}
	return nil
}

// ensureBearerFresh fails fast with an actionable message when the Lux
// identity bearer is an expired JWT. ensureLuxScope decodes claims but does
// not check expiry, and the auth identity token can be short-lived with no
// refresh token; without this an expired token passes preflight and surfaces
// later as a raw 401 from deep inside the topos critic. Opaque (non-JWT)
// tokens and tokens without an exp claim are skipped: Lux stays the authority.
func ensureBearerFresh(bearer string) error {
	claims := decodeJWTClaims(bearer)
	if claims == nil {
		return nil
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		return nil
	}
	if time.Now().Unix() >= int64(exp) {
		return fmt.Errorf("your Lux identity token has expired; run `latere auth login` and retry")
	}
	return nil
}

// mostRecentSession finds the newest Claude Code transcript under the
// project directory claude derives from cwd, returning its session ID and
// on-disk path. There is no importable "most recent" helper (cmd/agon
// always receives a session ID from the stop hook), so the discovery
// lives here for the manual invocation path.
func mostRecentSession(home, cwd string) (sessionID, path string, err error) {
	dir := filepath.Join(home, ".claude", "projects", input.EncodeCwd(cwd))
	entries, derr := os.ReadDir(dir)
	if derr != nil {
		return "", "", fmt.Errorf("no Claude sessions found for %s; run a Claude Code session here first, or pass --session <id>", cwd)
	}
	var newest os.DirEntry
	var newestMod time.Time
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if newest == nil || info.ModTime().After(newestMod) {
			newest, newestMod = e, info.ModTime()
		}
	}
	if newest == nil {
		return "", "", fmt.Errorf("no Claude sessions found for %s; run a Claude Code session here first, or pass --session <id>", cwd)
	}
	path = filepath.Join(dir, newest.Name())
	sessionID = newest.Name()[:len(newest.Name())-len(".jsonl")]
	return sessionID, path, nil
}

func printAgonSummary(cmd *cobra.Command, s *adversarial.Summary) {
	out := cmd.OutOrStdout()
	printWrappedField("termination", defaultStr(s.Termination, "-"))
	printWrappedField("forks", fmt.Sprintf("%d", len(s.Forks)))
	printWrappedField("unresolved", fmt.Sprintf("%d", s.Unresolved))
	printWrappedField("wall", fmt.Sprintf("%ds", s.WallSeconds))
	if s.SessionDir != "" {
		printWrappedField("session_dir", s.SessionDir)
	}
	if s.Headline != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, s.Headline)
	}
}
