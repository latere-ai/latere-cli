// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
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
'latere app deploys' lists them. One argument is the slug; without any, the
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

  git push latere main && latere app logs -f

Color codes in a line are printed as they are on a terminal and removed
otherwise. --json prints each line as the API's JSON, one per line.`,
		Example: `  latere app logs
  latere app logs -f
  latere app logs hello 5d2f8a1c
  latere app logs hello --json`,
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
			l := appLogs{
				client: newAppClient(*apiURL, *authURL),
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
				if d := l.deploy; d != nil && (d.Status == "waiting" || d.Status == "queued") {
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
	deploy *appDeploy
	// deploys is the app's list deploy was picked from.
	deploys []appDeploy
	// release is the deploy of a release whose log was asked for, when the
	// log read is that of the preview it released.
	release *appDeploy
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
	deploys, _, err := l.client.listDeploys(ctx, l.slug)
	if err != nil {
		return err
	}
	l.deploys = deploys
	if arg == "" && head != "" {
		return l.awaitCommit(ctx, deploys, head)
	}
	if arg == "" {
		if len(deploys) == 0 {
			return fmt.Errorf("%s has no deploys yet. A push builds one: git push %s main", l.slug, appRemote)
		}
		l.id, l.deploy = deploys[0].ID, &deploys[0]
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
			l.deploy = &deploys[i]
		}
	}
	return nil
}

// awaitCommit picks the newest deploy of the commit head, reading the
// deploys again until it appears or appDeployWait has passed.
func (l *appLogs) awaitCommit(ctx context.Context, deploys []appDeploy, head string) error {
	short := head[:min(7, len(head))]
	deadline := time.Now().Add(appDeployWait)
	for waited := false; ; waited = true {
		for i := range deploys {
			if deploys[i].CommitSHA == head {
				l.id, l.deploy, l.deploys = deploys[i].ID, &deploys[i], deploys
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("no deploy of %s, the commit HEAD names, appeared within %s.\nPush it with: git push %s <branch>\nOr name a deploy from 'latere app deploys': latere app logs %s <deploy>", short, appDeployWait, appRemote, l.slug)
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
		if deploys, _, err = l.client.listDeploys(ctx, l.slug); err != nil {
			return err
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
			l.deploy = &l.deploys[i]
			break
		}
	}
}

func (l *appLogs) path(follow bool) string {
	p := appPath(l.slug, "deploys", l.id, "logs")
	if follow {
		p += "?follow=1"
	}
	return p
}

// print writes one line of the log: the API's JSON with --json, else the
// time, the step and the text, with the component when the deploy builds
// more than one.
func (l *appLogs) print(data []byte) error {
	if l.json {
		fprintf(l.out, "%s\n", bytes.TrimSpace(data))
		return nil
	}
	var line appLogLine
	if err := json.Unmarshal(data, &line); err != nil {
		return fmt.Errorf("parse a build log line: %w", err)
	}
	text := line.Line
	if !l.raw {
		text = stripANSI(text)
	}
	ts := "--:--:--"
	if !line.TS.IsZero() {
		ts = line.TS.Local().Format("15:04:05")
	}
	if l.deploy == nil || len(l.deploy.Components) != 1 {
		fprintf(l.out, "%s %s %-8s %s\n", ts, line.Component, line.Src, text)
	} else {
		fprintf(l.out, "%s %-8s %s\n", ts, line.Src, text)
	}
	return nil
}

// stored prints the log stored so far, as NDJSON.
func (l *appLogs) stored(ctx context.Context) error {
	resp, err := l.client.stream(ctx, l.path(false), "application/x-ndjson")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxLogLine)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		if err := l.print(sc.Bytes()); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read the build log: %w", err)
	}
	return nil
}

// follow streams the log as server-sent events until the end event, and
// answers the build's outcome.
func (l *appLogs) follow(ctx context.Context) error {
	resp, err := l.client.stream(ctx, l.path(true), "text/event-stream")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxLogLine)
	var (
		event string
		data  []byte
	)
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case len(line) == 0:
			// A blank line dispatches the event its fields built.
			if event == "end" {
				return l.end(data)
			}
			if len(data) > 0 && (event == "" || event == "message") {
				if err := l.print(data); err != nil {
					return err
				}
			}
			event, data = "", nil
		case line[0] == ':':
			// A comment: the heartbeat a quiet build sends.
		default:
			field, value, _ := bytes.Cut(line, []byte(":"))
			value = bytes.TrimPrefix(value, []byte(" "))
			switch string(field) {
			case "event":
				event = string(value)
			case "data":
				if data != nil {
					data = append(data, '\n')
				}
				data = append(data, value...)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read the build log: %w", err)
	}
	if event == "end" {
		return l.end(data)
	}
	return fmt.Errorf("the build log of %s closed before the build ended; run the command again to follow it", shortID(l.id))
}

// end reads the stream's last event. A build that built answers nil; one
// that failed or was canceled answers its code and message.
func (l *appLogs) end(data []byte) error {
	var end appLogEnd
	if err := json.Unmarshal(data, &end); err != nil {
		return fmt.Errorf("parse the end of the build log: %w", err)
	}
	id := shortID(l.id)
	switch end.Status {
	// built is a finished build; ready and live are the deploy statuses past
	// it.
	case "built", "ready", "live":
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
	case "canceled":
		return fmt.Errorf("deploy %s was canceled", id)
	case "failed":
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
