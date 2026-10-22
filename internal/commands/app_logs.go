// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"latere.ai/x/sdk-go/apps"
)

// outputIsTerminal reports whether w is a terminal, where a log line's
// color codes are printed as they are. A variable so a test can stand in
// for a terminal.
var outputIsTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// ansiEscape matches a terminal escape sequence: a CSI sequence such as a
// color, an OSC sequence such as a title or a link, or a two-byte escape.
var ansiEscape = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|[@-Z\\\\-_])")

// stripANSI removes terminal escape sequences from s.
func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiEscape.ReplaceAllString(s, "")
}

func newAppLogsCmd(apiURL, authURL *string) *cobra.Command {
	var follow, jsonF bool
	cmd := &cobra.Command{
		Use:   "logs [slug] [deploy]",
		Short: "Print or follow a deploy's build log.",
		Long: `Print the build log of a deploy, the newest one unless [deploy] names
another by its id or a prefix of at least 8 hexadecimal digits, as
'latere apps deploys' lists them. One argument is the slug; without any, the
slug is read from the git remote latere.

The deploy of a release builds nothing: it serves the build of the preview it
released. For it, the command says so on one line and prints that preview's
build log, under the same rules.

--follow streams the log while the deploy builds and exits when the build
ends: 0 when it built, and 1 with the failure's code and message when it
failed or was canceled. When the slug is read from the git remote and no
[deploy] is given, it follows the newest deploy of the commit HEAD names, and
waits up to a minute for that deploy to appear, since a push creates it a
moment after the push returns. A deploy that has not started building yet
says so before its first line. That makes a push and its build one command:

  git push latere main && latere apps logs -f

Color codes in a line are printed as they are on a terminal and removed
otherwise. --json prints each line as the API's JSON, one per line.`,
		Example: `  latere apps logs
  latere apps logs -f
  latere apps logs hello 5d2f8a1c
  latere apps logs hello --json`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, fromRemote, err := resolveAppSlug(cmd, firstArg(args))
			if err != nil {
				return err
			}
			deployArg := ""
			if len(args) == 2 {
				deployArg = args[1]
			}
			c, err := newAppClient(*apiURL, *authURL)
			if err != nil {
				return err
			}
			l := appLogs{
				client: c,
				slug:   slug, json: jsonF,
				out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr(),
			}
			l.raw = jsonF || outputIsTerminal(l.out)
			// After a push, the deploy to follow is the one of the commit
			// pushed, which the newest deploy may not be yet.
			head := ""
			if follow && fromRemote && deployArg == "" {
				head = headCommit(cmd.Context())
			}
			if err := l.resolve(cmd.Context(), deployArg, head); err != nil {
				return withSlugSource(err, slug, fromRemote)
			}
			l.toSource()
			if follow {
				if d := l.deploy; d != nil && (d.Status == apps.DeployWaiting || d.Status == apps.DeployQueued) {
					fprintln(l.errOut, "Waiting for the build to start...")
				}
				return l.follow(cmd.Context())
			}
			return l.stored(cmd.Context())
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream the log until the build ends, and exit with its outcome")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print each line as JSON")
	return cmd
}

// appLogs is one run of `app logs`: the deploy it reads and where it
// writes.
type appLogs struct {
	client *appClient
	slug   string
	// id is the deploy's id or the prefix given for it.
	id string
	// deploy is the deploy id names, when the app's list has it.
	deploy *apps.Deploy
	// deploys is the app's list deploy was picked from.
	deploys []*apps.Deploy
	// release is the deploy of a release whose log was asked for, when the
	// log read is that of the preview it released.
	release *apps.Deploy
	json    bool
	// raw keeps a line's escape sequences: on a terminal, and in JSON.
	raw         bool
	out, errOut io.Writer
}

// appDeployWait is how long `logs -f` waits for the deploy of the commit
// HEAD names to appear, and appDeployPoll how often it reads the deploys
// meanwhile. A push returns before the deploy it creates is recorded.
var (
	appDeployWait = time.Minute
	appDeployPoll = 2 * time.Second
)

// headCommit is the commit HEAD names in the current directory's
// repository, or "" when there is none to read.
func headCommit(ctx context.Context) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	sha, err := gitIn(ctx, dir, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return sha
}

// resolve picks the deploy: the one the argument names, which the API
// resolves when it is a prefix; else the newest of the commit head, waiting
// for it to appear, when head is given; else the newest.
func (l *appLogs) resolve(ctx context.Context, arg, head string) error {
	deploys, err := l.client.apps.ListDeploys(ctx, l.slug)
	if err != nil {
		return l.client.refused(err)
	}
	l.deploys = deploys
	if arg == "" && head != "" {
		return l.awaitCommit(ctx, deploys, head)
	}
	if arg == "" {
		if len(deploys) == 0 {
			return fmt.Errorf("%s has no deploys yet. A push builds one: git push %s main", l.slug, appRemote)
		}
		l.id, l.deploy = deploys[0].ID, deploys[0]
		return nil
	}
	l.id = arg
	prefix := strings.ToLower(strings.ReplaceAll(arg, "-", ""))
	for i := range deploys {
		if strings.HasPrefix(strings.ReplaceAll(deploys[i].ID, "-", ""), prefix) {
			if l.deploy != nil {
				l.deploy = nil // two deploys share it; the API refuses it
				break
			}
			l.deploy = deploys[i]
		}
	}
	return nil
}

// awaitCommit picks the newest deploy of the commit head, reading the
// deploys again until it appears or appDeployWait has passed.
func (l *appLogs) awaitCommit(ctx context.Context, deploys []*apps.Deploy, head string) error {
	short := head[:min(7, len(head))]
	deadline := time.Now().Add(appDeployWait)
	for waited := false; ; waited = true {
		for i := range deploys {
			if deploys[i].CommitSHA == head {
				l.id, l.deploy, l.deploys = deploys[i].ID, deploys[i], deploys
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("no deploy of %s, the commit HEAD names, appeared within %s.\nPush it with: git push %s <branch>\nOr name a deploy from 'latere apps deploys': latere apps logs %s <deploy>", short, appDeployWait, appRemote, l.slug)
		}
		if !waited {
			fprintf(l.errOut, "Waiting for the deploy of %s...\n", short)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(appDeployPoll, time.Until(deadline))):
		}
		var err error
		if deploys, err = l.client.apps.ListDeploys(ctx, l.slug); err != nil {
			return l.client.refused(err)
		}
	}
}

// toSource turns the deploy of a release into the preview it released. A
// release's deploy serves that preview's build and builds nothing, so its
// own log is empty; the log that says what was built is the preview's.
func (l *appLogs) toSource() {
	d := l.deploy
	if d == nil || d.FromDeploy == "" {
		return
	}
	fprintf(l.errOut, "Deploy %s released %s from preview %s without a build; its build log:\n", shortID(d.ID), refName(d.Ref), shortID(d.FromDeploy))
	l.release, l.id, l.deploy = d, d.FromDeploy, nil
	for i := range l.deploys {
		if l.deploys[i].ID == d.FromDeploy {
			l.deploy = l.deploys[i]
			break
		}
	}
}

// print writes one line of the log: the API's JSON with --json, else the
// time, the step and the text, with the component when the deploy builds
// more than one.
func (l *appLogs) print(line *apps.LogLine) {
	if l.json {
		fprintf(l.out, "%s\n", bytes.TrimSpace(line.RawJSON()))
		return
	}
	text := line.Text
	if !l.raw {
		text = stripANSI(text)
	}
	ts := "--:--:--"
	if !line.Time.IsZero() {
		ts = line.Time.Local().Format("15:04:05")
	}
	if l.deploy == nil || len(l.deploy.Components) != 1 {
		fprintf(l.out, "%s %s %-8s %s\n", ts, line.Component, line.Step, text)
	} else {
		fprintf(l.out, "%s %-8s %s\n", ts, line.Step, text)
	}
}

// refused is what the command says of an error reading the log answered:
// a line that could not be read, or a line longer than the SDK reads, in
// its own words, and the rest as every app command says it.
func (l *appLogs) refused(err error) error {
	if e := jsonError(err); e != nil {
		return fmt.Errorf("parse the build log: %w", e)
	}
	if errors.Is(err, bufio.ErrTooLong) {
		return fmt.Errorf("read the build log: %w", bufio.ErrTooLong)
	}
	return l.client.refused(err)
}

// stored prints the log stored so far.
func (l *appLogs) stored(ctx context.Context) error {
	for line, err := range l.client.apps.ReadDeployLog(ctx, l.slug, l.id, nil) {
		if err != nil {
			return l.refused(err)
		}
		l.print(line)
	}
	return nil
}

// follow streams the log until its end, and answers the build's outcome. A
// stream that drops is opened again after the last line read, as the SDK
// does for a stream, for as long as the API sends anything on it; one that
// keeps closing with nothing sent before the build ends is reported once
// the SDK's retries are spent.
func (l *appLogs) follow(ctx context.Context) error {
	for ev, err := range l.client.apps.FollowDeployLog(ctx, l.slug, l.id, nil) {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return l.refused(err)
		}
		if ev.End != nil {
			return l.end(ev.End)
		}
		l.print(ev.Line)
	}
	return fmt.Errorf("the build log of %s closed before the build ended; run the command again to follow it", shortID(l.id))
}

// end reads the stream's last event. A build that built answers nil; one
// that failed or was canceled answers its code and message.
func (l *appLogs) end(end *apps.LogEnd) error {
	id := shortID(l.id)
	switch end.Status {
	// built is a finished build; ready and live are the deploy statuses past
	// it.
	case apps.BuildBuilt, "ready", "live":
		fprintf(l.errOut, "Deploy %s built.\n", id)
		if r := l.release; r != nil {
			if r.URL != "" {
				fprintf(l.errOut, "Released as %s at %s\n", refName(r.Ref), r.URL)
			}
		} else if d := l.deploy; d != nil {
			if d.Preview && d.PreviewURL != "" {
				fprintf(l.errOut, "Preview: %s\n", d.PreviewURL)
			} else if !d.Preview && d.URL != "" {
				fprintf(l.errOut, "Address: %s\n", d.URL)
			}
		}
		return nil
	case apps.BuildCanceled:
		return fmt.Errorf("deploy %s was canceled", id)
	case apps.BuildFailed:
		if end.Error == nil {
			return fmt.Errorf("deploy %s failed", id)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "deploy %s failed", id)
		if end.Error.Component != "" {
			fmt.Fprintf(&b, " in %s", end.Error.Component)
		}
		fmt.Fprintf(&b, ": %s: %s", defaultStr(end.Error.Code, "failed"), end.Error.Message)
		if end.Error.Hint != "" {
			b.WriteString("\n" + end.Error.Hint)
		}
		return errors.New(b.String())
	default:
		return fmt.Errorf("the build log of %s ended with the status %q", id, end.Status)
	}
}
