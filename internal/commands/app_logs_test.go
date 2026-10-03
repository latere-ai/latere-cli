// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestAppLogsStoredOfTheNewestDeploy(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "logs", "hello")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "fetch    fetching 3f2a9c1 for app\n", "build    compiled 14 files\n")
	if strings.Contains(out, "\x1b") {
		t.Errorf("escape codes reached a non-terminal:\n%q", out)
	}
	seen := s.seen()
	if seen[1] != "GET /v1/apps/apps/hello/deploys/"+stubPreviewID+"/logs" || s.accepts[1] != "application/x-ndjson" {
		t.Errorf("requests = %v, accept %v", seen, s.accepts)
	}
}

func TestAppLogsKeepsColorOnATerminal(t *testing.T) {
	restore := outputIsTerminal
	outputIsTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { outputIsTerminal = restore })
	s := newStubApps(t)
	out, _, err := runApp(t, s, "logs", "hello")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "\x1b[32mcompiled\x1b[0m 14 files")
}

func TestAppLogsJSONIsTheAPIsLines(t *testing.T) {
	s := newStubApps(t)
	out, _, err := runApp(t, s, "logs", "hello", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if out != s.logs {
		t.Errorf("json =\n%q\nwant\n%q", out, s.logs)
	}
}

func TestAppLogsOfANamedDeploy(t *testing.T) {
	s := newStubApps(t)
	if _, _, err := runApp(t, s, "logs", "hello", "0b1c2d3e"); err != nil {
		t.Fatal(err)
	}
	if got := s.seen()[1]; got != "GET /v1/apps/apps/hello/deploys/0b1c2d3e/logs" {
		t.Errorf("request = %q", got)
	}
}

func TestAppLogsOfATooShortPrefix(t *testing.T) {
	s := newStubApps(t)
	s.logStatus = 400
	s.logBody = `{"error":{"code":"id_too_short","message":"A deploy id needs at least 8 characters.","details":{"deploy_id":"0b1c","min":8,"hint":"Use 8 or more characters of the id."}}}`
	_, _, err := runApp(t, s, "logs", "hello", "0b1c")
	if err == nil || err.Error() != "id_too_short: A deploy id needs at least 8 characters.\nUse 8 or more characters of the id." {
		t.Errorf("logs = %v", err)
	}
}

func TestAppLogsOfAnAppWithNoDeploys(t *testing.T) {
	s := newStubApps(t)
	s.deploys["hello"] = `{"deploys":[]}`
	if _, _, err := runApp(t, s, "logs", "hello", "-f"); err == nil || !strings.Contains(err.Error(), "hello has no deploys yet") {
		t.Errorf("logs = %v", err)
	}
}

// sseLine is one log line as an event of the followed stream.
func sseLine(seq int, line string) string {
	b, _ := json.Marshal(map[string]any{"seq": seq, "ts": "2026-09-22T12:00:01Z", "src": "build", "component": "app", "line": line})
	return fmt.Sprintf("id: app:%d\ndata: %s\n\n", seq, b)
}

func TestAppLogsFollowEndsWithTheBuild(t *testing.T) {
	for _, status := range []string{"built", "ready", "live"} {
		t.Run(status, func(t *testing.T) {
			s := newStubApps(t)
			s.frames = []string{": heartbeat\n\n", sseLine(1, "npm run build"), ": heartbeat\n\n", ": heartbeat\n\n",
				sseLine(2, "\x1b[1mdone\x1b[0m"), "event: end\ndata: {\"status\":\"" + status + "\"}\n\n"}
			s.frameDelay = 10 * time.Millisecond
			out, errOut, err := runApp(t, s, "logs", "hello", "-f")
			if err != nil {
				t.Fatalf("logs -f = %v", err)
			}
			if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 2 || !strings.HasSuffix(lines[0], "build    npm run build") || !strings.HasSuffix(lines[1], "build    done") {
				t.Errorf("stdout =\n%q", out)
			}
			wantContains(t, errOut, "Deploy 5d2f8a1c built.\n", "Preview: https://5d2f8a1c--hello.latere.site\n")
			if got := s.seen()[1]; got != "GET /v1/apps/apps/hello/deploys/"+stubPreviewID+"/logs?follow=1" || s.accepts[1] != "text/event-stream" {
				t.Errorf("request = %q, accept %q", got, s.accepts[1])
			}
		})
	}
}

func TestAppLogsFollowOfAFailedBuild(t *testing.T) {
	s := newStubApps(t)
	s.frames = []string{sseLine(1, "npm ERR! missing script: build"), ": heartbeat\n\n",
		"event: end\ndata: {\"status\":\"failed\",\"error\":{\"code\":\"build_failed\",\"message\":\"The build command failed.\",\"hint\":\"Read the build log; the failing line is near the end.\",\"component\":\"app\"}}\n\n"}
	out, _, err := runApp(t, s, "logs", "hello", "--follow")
	if err == nil {
		t.Fatal("want the failure")
	}
	wantContains(t, out, "npm ERR! missing script: build")
	var printed bytes.Buffer
	if code := HandleExitError(&printed, err); code == 0 {
		t.Error("a failed build exits 0")
	}
	if got := printed.String(); got != "deploy 5d2f8a1c failed in app: build_failed: The build command failed.\nRead the build log; the failing line is near the end.\n" {
		t.Errorf("printed = %q", got)
	}
}

func TestAppLogsFollowOfACanceledBuild(t *testing.T) {
	s := newStubApps(t)
	s.frames = []string{"event: end\ndata: {\"status\":\"canceled\"}\n\n"}
	if _, _, err := runApp(t, s, "logs", "hello", "-f"); err == nil || err.Error() != "deploy 5d2f8a1c was canceled" {
		t.Errorf("logs -f = %v", err)
	}
}

func TestAppLogsFollowOfAStreamThatClosesEarly(t *testing.T) {
	s := newStubApps(t)
	s.frames = []string{sseLine(1, "npm run build"), ": heartbeat\n\n"}
	if _, _, err := runApp(t, s, "logs", "hello", "-f"); err == nil || !strings.Contains(err.Error(), "closed before the build ended") {
		t.Errorf("logs -f = %v", err)
	}
}

// The end event's fields arrive in any order, and a stream that closes right
// after its data line still ends.
func TestAppLogsFollowEndWithoutATrailingBlankLine(t *testing.T) {
	s := newStubApps(t)
	s.frames = []string{"data: {\"status\":\"built\"}\nevent: end\n"}
	if _, _, err := runApp(t, s, "logs", "hello", "-f"); err != nil {
		t.Errorf("logs -f = %v", err)
	}
}

func TestAppLogsFollowJSON(t *testing.T) {
	s := newStubApps(t)
	s.frames = []string{sseLine(1, "\x1b[1mone\x1b[0m"), ": heartbeat\n\n", "event: end\ndata: {\"status\":\"built\"}\n\n"}
	out, _, err := runApp(t, s, "logs", "hello", "-f", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var line appLogLine
	if err := json.Unmarshal([]byte(out), &line); err != nil || line.Line != "\x1b[1mone\x1b[0m" || line.Seq != 1 {
		t.Errorf("json = %q %v", out, err)
	}
}

func TestAppLogsLabelsTheComponentsOfAMultiComponentDeploy(t *testing.T) {
	s := newStubApps(t)
	s.deploys["hello"] = `{"deploys":[` + stubDeployJSON(stubPreviewID, "building", "refs/heads/main", true,
		`[{"name":"web","kind":"static","route":"/","status":"building"},{"name":"api","kind":"service","route":"/api","status":"building"}]`) + `]}`
	out, _, err := runApp(t, s, "logs", "hello")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, " app fetch    fetching")
}

func TestStripANSI(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                                 "plain",
		"\x1b[31mred\x1b[0m":                    "red",
		"\x1b[1;38;5;208mbold\x1b[m":            "bold",
		"\x1b]8;;https://x\x07link\x1b]8;;\x07": "link",
		"\x1b]0;title\x1b\\text":                "text",
		"a\x1bMb":                               "ab",
	} {
		if got := stripANSI(in); got != want {
			t.Errorf("stripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}

// gitCommit makes an empty commit in dir and answers its id.
func gitCommit(t *testing.T, dir string) string {
	t.Helper()
	gitRun(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-q", "-m", "init")
	return gitRun(t, dir, "rev-parse", "HEAD")
}

// stubDeployOf is a preview deploy of commit sha.
func stubDeployOf(id, sha, status string) string {
	return strings.Replace(stubDeployJSON(id, status, "refs/heads/main", true, oneComponent),
		"3f2a9c1b7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a", sha, 1)
}

// shortenDeployWait makes `logs -f` wait for the deploy of HEAD for wait,
// reading the deploys every millisecond.
func shortenDeployWait(t *testing.T, wait time.Duration) {
	t.Helper()
	w, p := appDeployWait, appDeployPoll
	appDeployWait, appDeployPoll = wait, time.Millisecond
	t.Cleanup(func() { appDeployWait, appDeployPoll = w, p })
}

// After a push, the newest deploy is still the previous one until the push's
// deploy is recorded; following it would report a build that already ended.
func TestAppLogsFollowWaitsForTheDeployOfHEAD(t *testing.T) {
	shortenDeployWait(t, 10*time.Second)
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
	head := gitCommit(t, dir)
	const pushedID = "9e8d7c6b-5a49-4382-9716-a5b4c3d2e1f0"
	previous := stubDeployOf(stubPreviewID, "1111111111111111111111111111111111111111", "ready")
	s.deploysSeq = []string{
		`{"deploys":[` + previous + `]}`,
		`{"deploys":[` + previous + `]}`,
		`{"deploys":[` + stubDeployOf(pushedID, head, "queued") + `,` + previous + `]}`,
	}
	s.frames = []string{sseLine(1, "npm run build"), "event: end\ndata: {\"status\":\"built\"}\n\n"}
	_, errOut, err := runAppIn(t, s, dir, "", "logs", "-f")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, errOut, "Waiting for the deploy of "+head[:7]+"...\n", "Deploy 9e8d7c6b built.", "Preview: https://9e8d7c6b--hello.latere.site")
	seen := s.seen()
	if last := seen[len(seen)-1]; last != "GET /v1/apps/apps/hello/deploys/"+pushedID+"/logs?follow=1" {
		t.Errorf("requests = %v", seen)
	}
}

func TestAppLogsFollowFindsTheDeployOfHEADAtOnce(t *testing.T) {
	shortenDeployWait(t, 10*time.Second)
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
	head := gitCommit(t, dir)
	// The newest deploy is another commit's; the one of HEAD is older.
	s.deploys["hello"] = `{"deploys":[` + stubDeployOf(stubReleaseID, "2222222222222222222222222222222222222222", "building") + `,` + stubDeployOf(stubPreviewID, head, "ready") + `]}`
	s.frames = []string{"event: end\ndata: {\"status\":\"built\"}\n\n"}
	_, errOut, err := runAppIn(t, s, dir, "", "logs", "-f")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut, "Waiting") {
		t.Errorf("waited for a deploy the list had: %q", errOut)
	}
	if got := s.seen()[1]; got != "GET /v1/apps/apps/hello/deploys/"+stubPreviewID+"/logs?follow=1" {
		t.Errorf("request = %q", got)
	}
}

func TestAppLogsFollowGivesUpOnTheDeployOfHEAD(t *testing.T) {
	shortenDeployWait(t, 20*time.Millisecond)
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
	head := gitCommit(t, dir)
	s.deploysSeq = []string{`{"deploys":[]}`}
	_, _, err := runAppIn(t, s, dir, "", "logs", "-f")
	if err == nil {
		t.Fatal("want a refusal")
	}
	wantContains(t, err.Error(), "no deploy of "+head[:7]+", the commit HEAD names, appeared within 20ms", "git push latere <branch>", "latere app logs hello <deploy>")
	for _, r := range s.seen() {
		if strings.Contains(r, "/logs") {
			t.Errorf("opened a log: %v", s.seen())
		}
	}
}

// The wait is for a push's deploy: a named slug, a named deploy and the
// stored log each read the deploys once.
func TestAppLogsWaitsOnlyWhenFollowingThePushedCommit(t *testing.T) {
	shortenDeployWait(t, 10*time.Second)
	for _, args := range [][]string{{"logs", "-f", "hello"}, {"logs", "-f", "hello", "5d2f8a1c"}, {"logs"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := newStubApps(t)
			dir := gitRepo(t)
			gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
			gitCommit(t, dir)
			s.frames = []string{"event: end\ndata: {\"status\":\"built\"}\n\n"}
			_, errOut, err := runAppIn(t, s, dir, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(errOut, "Waiting") || s.deploysReads != 1 {
				t.Errorf("stderr %q, deploy reads %d", errOut, s.deploysReads)
			}
		})
	}
}

// A repository without a commit has no HEAD to wait for; the newest deploy
// is followed.
func TestAppLogsFollowInARepositoryWithoutCommits(t *testing.T) {
	s := newStubApps(t)
	dir := gitRepo(t)
	gitRun(t, dir, "remote", "add", "latere", "https://code.latere.ai/u-1/hello.git")
	s.frames = []string{"event: end\ndata: {\"status\":\"built\"}\n\n"}
	if _, _, err := runAppIn(t, s, dir, "", "logs", "-f"); err != nil {
		t.Fatal(err)
	}
	if got := s.seen()[1]; got != "GET /v1/apps/apps/hello/deploys/"+stubPreviewID+"/logs?follow=1" {
		t.Errorf("request = %q", got)
	}
}
