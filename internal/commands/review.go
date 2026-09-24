// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"latere.ai/x/pkg/luxsdk"
	xtopos "latere.ai/x/topos"
	adversarial "latere.ai/x/topos/adversarial"
	"latere.ai/x/topos/adversarial/claude"
	"latere.ai/x/topos/adversarial/critic"
	"latere.ai/x/topos/adversarial/input"

	"github.com/latere-ai/latere-cli/internal/reviews"
)

// review runs adversarial review locally on the developer machine. The
// proposer forks the developer's real Claude Code session
// (claude --resume <id> --fork-session, full fidelity), while the critics
// run through topos with their model calls sent to the core's OpenAI door
// at the origin with the model key, so critic cost is drawn from the key's
// context and no provider key is needed locally.
//
// See specs/002-review-local-subcommand.md and
// specs/007-models-at-the-origin.md.

// reviewOpts holds the resolved flags for one `latere review` invocation.
type reviewOpts struct {
	session   string
	dir       string
	stateDir  string
	forks     int
	maxRounds int
	costCap   int
	model     string
	propTO    time.Duration
	modelsURL string
	authURL   string
}

func newReviewCmd() *cobra.Command {
	var o reviewOpts
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Adversarial review of your latest Claude Code session.",
		Long: `Run an adversarial debate over the working-tree diff of your most
recent Claude Code session.

The proposer forks your real Claude Code session
(claude --resume <id> --fork-session) so it argues with the full
transcript, harness context, and working tree. The critics run through
topos and call their model through the Latere API with your model key
(see 'latere models key'), so critic cost is drawn from your current
context with no provider key needed locally.

Run 'latere login' first to sign in. The proposer additionally needs
the 'claude' CLI installed and authenticated.

By default it reviews the most recent session under the working
directory; pass --session <id> to pick a specific one.

Review logs are written to a user-global state dir
($XDG_STATE_HOME/latere/reviews/<repo-key>/), not into the reviewed
repo. Old sessions are pruned automatically; --state-dir overrides.`,
		Example: `  latere review
  latere review --forks 3 --max-rounds 6
  latere review --session 4f3c2b1a --dir ~/code/myrepo`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReview(cmd.Context(), cmd, &o)
		},
	}
	cmd.Flags().StringVar(&o.session, "session", "", "Claude Code session ID to review (default: most recent under --dir)")
	cmd.Flags().StringVar(&o.dir, "dir", ".", "working directory: git repo root and Claude session home")
	cmd.Flags().StringVar(&o.stateDir, "state-dir", "", "where to write review logs (default: $XDG_STATE_HOME/latere/reviews/<repo-key>/)")
	cmd.Flags().IntVar(&o.forks, "forks", 1, "number of independent critic forks")
	cmd.Flags().IntVar(&o.maxRounds, "max-rounds", 4, "per-fork internal-round cap")
	cmd.Flags().IntVar(&o.costCap, "cost-cap", 50000, "soft token budget (proposer tokens; topos critics report no usage yet)")
	cmd.Flags().StringVar(&o.model, "model", defaultCatalogModel, "critic model, as 'latere models' lists it")
	cmd.Flags().DurationVar(&o.propTO, "proposer-timeout", 5*time.Minute, "per-round deadline for the proposer's claude call (large sessions may need more)")
	cmd.Flags().StringVar(&o.modelsURL, "models-url", "", "override the models base URL (overrides LATERE_MODELS_URL)")
	cmd.Flags().StringVar(&o.authURL, "auth-url", "", "override the auth base URL (default derived from the models URL)")
	return cmd
}

// reviewCriticModel is the critics' model connection: the core's OpenAI door
// in luxsdk's direct mode, which translates the critic's requests to Chat
// Completions on this machine, with the model key as the bearer.
func reviewCriticModel(model, modelsURL string, key func(context.Context) (string, error)) xtopos.ModelOptions {
	return xtopos.ModelOptions{
		Kind:         xtopos.ModelDirect,
		Provider:     string(luxsdk.ProviderOpenAI),
		Model:        model,
		BaseURL:      resolveModelsURL(modelsURL) + openAIDoor,
		BearerSource: key,
	}
}

func runReview(ctx context.Context, cmd *cobra.Command, o *reviewOpts) error {
	cwd, err := filepath.Abs(o.dir)
	if err != nil {
		return fmt.Errorf("resolve --dir: %w", err)
	}
	// Default the review-log location to a user-global XDG state dir keyed by
	// the reviewed repo, so review logs stay out of the working tree. An
	// explicit --state-dir overrides and is left untouched by retention.
	stateDir := o.stateDir
	if stateDir == "" {
		stateDir = reviews.Dir(gitToplevel(ctx, cwd))
		if stateDir == "" {
			// No state home resolved; degrade to cwd rather than fail.
			stateDir = cwd
		}
		reviews.Prune(stateDir)
	}

	// Have the core accept the model key up front: this checks that the user
	// is signed in, and waits out a key created now, before a proposer round
	// is spent. The same source is handed to topos, which presents the
	// accepted key on every critic call.
	keySource := modelKeySource(o.modelsURL, o.authURL)
	if _, err := keySource(ctx); err != nil {
		return err
	}

	// Locate the session to fork. The proposer needs a real Claude session
	// ID; without one there is nothing to --resume.
	sessionID := o.session
	var transcriptPath string
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
		fprintf(cmd.ErrOrStderr(), "[review] reviewing most recent session %s\n", sessionID)
	} else {
		path, perr := input.LocateTranscript(home, cwd, sessionID, "")
		if perr != nil {
			return fmt.Errorf("%w (pass --session <id> for a session under a different dir)", perr)
		}
		transcriptPath = path
	}

	// Compute the diff and gate. When the tree is clean (claude already
	// committed), fall back to reviewing the last commit via HEAD~1..HEAD.
	diff, err := input.Compute(ctx, input.DiffSpec{From: "HEAD", To: ".", Cwd: cwd})
	if err != nil {
		return err
	}
	if diff.ChangedLines == 0 {
		if fb, fbErr := input.Compute(ctx, input.DiffSpec{From: "HEAD~1", To: "HEAD", Cwd: cwd}); fbErr == nil && fb.ChangedLines > 0 {
			fprintln(cmd.ErrOrStderr(), "[review] working tree clean; falling back to HEAD~1..HEAD")
			diff = fb
		}
	}
	if input.Trivial(diff, 0) {
		fprintln(cmd.ErrOrStderr(), "[review] no substantive diff to review")
		return nil
	}

	// Task context from the session's first user turn (best-effort).
	taskCtx := "(task context unavailable)"
	if tr, rerr := input.ReadTranscript(transcriptPath); rerr == nil && tr.FirstUser != "" {
		taskCtx = tr.FirstUser
	}

	proposer := claude.NewProposer(sessionID, cwd, claude.WithProposerDeadline(o.propTO))
	critics := critic.NewCriticFactory(critic.Config{
		Model: reviewCriticModel(o.model, o.modelsURL, keySource),
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
	printReviewSummary(cmd, summary)
	// Verdict in the exit code: a completed debate with open attacks exits
	// non-zero so it can gate CI/hooks, distinct from a command error. main
	// maps this sentinel to exit 2.
	if summary.Unresolved > 0 {
		return &unresolvedError{n: summary.Unresolved}
	}
	return nil
}

// gitToplevel resolves the git repository root for cwd so the review-log key is
// stable no matter which subdirectory the command is run from. It falls back to
// cwd when cwd is not inside a git work tree (or git is unavailable), so a
// non-repo run still gets a deterministic, repo-scoped key.
func gitToplevel(ctx context.Context, cwd string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return cwd
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return cwd
	}
	return top
}

// mostRecentSession finds the newest Claude Code transcript under the
// project directory claude derives from cwd, returning its session ID and
// on-disk path. The engine has no importable "most recent" helper (it takes a
// session ID directly), so the discovery lives here for the manual invocation
// path where the user has not passed --session.
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

func printReviewSummary(cmd *cobra.Command, s *adversarial.Summary) {
	out := cmd.OutOrStdout()
	printWrappedField("termination", defaultStr(s.Termination, "-"))
	printWrappedField("forks", fmt.Sprintf("%d", len(s.Forks)))
	printWrappedField("unresolved", fmt.Sprintf("%d", s.Unresolved))
	printWrappedField("wall", fmt.Sprintf("%ds", s.WallSeconds))
	if s.SessionDir != "" {
		printWrappedField("session_dir", s.SessionDir)
	}
	if s.Headline != "" {
		fprintln(out)
		fprintln(out, s.Headline)
	}
}
