// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	cellaclient "latere.ai/x/cella/client"
)

func TestCellaExecRunsToCompletion(t *testing.T) {
	f := newFakeCore(t)
	f.exec = cellaclient.ExecResult{ExitCode: 7, Stdout: "out\n", Stderr: "err\n", Truncated: true}
	out, errOut, err := f.runCella("", "exec", "dev", "--cwd", "app", "--env", "DEBUG=1", "--timeout", "30s", "--", "make", "test")
	if code := exitCode(err); code != 7 {
		t.Fatalf("exit code = %d (%v), want the command's 7", code, err)
	}
	if out != "out\n" || !strings.HasPrefix(errOut, "err\n") || !strings.Contains(errOut, "cut at the control plane's one mebibyte cap") {
		t.Errorf("stdout=%q stderr=%q", out, errOut)
	}
	req := f.last("POST", "/sandboxes/dev/exec")
	if req.String() != "POST /sandboxes/dev/exec?wait=1" {
		t.Errorf("exec request = %s, want the synchronous route", req)
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatal(err)
	}
	want := `{"command":["make","test"],"env":{"DEBUG":"1"},"timeout":"30s","workdir":"/workspace/app"}`
	if got, _ := json.Marshal(body); string(got) != want {
		t.Errorf("exec body = %s, want %s", got, want)
	}
}

func TestCellaExecSuccess(t *testing.T) {
	f := newFakeCore(t)
	f.exec = cellaclient.ExecResult{Stdout: "Linux\n"}
	out, errOut, err := f.runCella("", "exec", "dev", "--", "uname")
	if err != nil || out != "Linux\n" || errOut != "" {
		t.Fatalf("exec = %q, %q, %v", out, errOut, err)
	}
}

func TestCellaExecRefusesBadFlagsBeforeSending(t *testing.T) {
	f := newFakeCore(t)
	for _, args := range [][]string{
		{"exec", "dev", "--timeout", "2h", "--", "true"},
		{"exec", "dev", "--env", "NOEQUALS", "--", "true"},
	} {
		if _, _, err := f.runCella("", args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
	if got := f.seen(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}

// run --ephemeral --rm is the create-then-use flow: a held create under a
// name the CLI picks, the command, and the delete.
func TestCellaRunEphemeral(t *testing.T) {
	f := newFakeCore(t)
	f.exec = cellaclient.ExecResult{Stdout: "hello\n"}
	out, errOut, err := f.runCella("", "run", "--ephemeral", "--rm", "--image", "gui", "--cpu", "2", "--memory", "4Gi", "--disk", "20", "--", "echo", "hello")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	reqs := f.all()
	if len(reqs) != 3 {
		t.Fatalf("requests = %v, want create, exec, delete", f.seen())
	}
	name := strings.TrimPrefix(reqs[0].Path, "/sandboxes/")
	if !strings.HasPrefix(name, "run-") || len(name) != len("run-")+12 {
		t.Fatalf("create path = %s, want a name the CLI picked", reqs[0].Path)
	}
	want := []string{
		"PUT /sandboxes/" + name + "?timeout=10m0s&wait=1",
		"POST /sandboxes/" + name + "/exec?wait=1",
		"DELETE /sandboxes/" + name,
	}
	if got := f.seen(); !slices.Equal(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	var manifest struct {
		Metadata struct{ Name string }
		Spec     struct {
			Image     string
			Resources struct{ CPU, Memory, Disk string }
			Lifecycle struct {
				AutoStop string `json:"autoStop"`
				TTL      string `json:"ttl"`
			}
		}
	}
	if err := json.Unmarshal(reqs[0].Body, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Metadata.Name != name || manifest.Spec.Image != "gui" ||
		manifest.Spec.Resources.CPU != "2" || manifest.Spec.Resources.Memory != "4Gi" || manifest.Spec.Resources.Disk != "20Gi" ||
		manifest.Spec.Lifecycle.AutoStop != "15m" || manifest.Spec.Lifecycle.TTL != "2h" {
		t.Errorf("manifest = %s", reqs[0].Body)
	}
	if out != "hello\n" || !strings.Contains(errOut, "created cella "+name) || !strings.Contains(errOut, "deleted cella "+name) {
		t.Errorf("stdout=%q stderr=%q", out, errOut)
	}
}

func TestCellaRunEphemeralJSON(t *testing.T) {
	f := newFakeCore(t)
	f.exec = cellaclient.ExecResult{ExitCode: 2, Stdout: "o", Stderr: "e", DurationMS: 12}
	out, errOut, err := f.runCella("", "run", "--ephemeral", "--rm", "--json", "--", "false")
	if code := exitCode(err); code != 2 {
		t.Fatalf("exit code = %d (%v), want 2", code, err)
	}
	var res oneShotResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("run --json = %q: %v", out, err)
	}
	if !strings.HasPrefix(res.Sandbox, "run-") || res.ID != "sbx-"+res.Sandbox || res.ExitCode != 2 || res.Stdout != "o" || res.Stderr != "e" || res.DurationMS != 12 {
		t.Errorf("run --json = %+v", res)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want nothing beside the JSON", errOut)
	}
}

// The sandbox is deleted on every path after the create: a failed start, a
// failed command, and an answer that never arrived.
func TestCellaRunEphemeralDeletesOnFailure(t *testing.T) {
	t.Run("failed start", func(t *testing.T) {
		f := newFakeCore(t)
		f.heldPhase, f.heldReason = cellaFailed, "Unschedulable"
		_, _, err := f.runCella("", "run", "--ephemeral", "--rm", "--", "true")
		if err == nil || !strings.Contains(err.Error(), "Unschedulable") {
			t.Fatalf("err = %v, want the start failure", err)
		}
		got := f.seen()
		if len(got) != 2 || !strings.HasPrefix(got[1], "DELETE /sandboxes/run-") {
			t.Errorf("requests = %v, want the create then the delete, no exec", got)
		}
	})
	t.Run("still starting", func(t *testing.T) {
		f := newFakeCore(t)
		f.heldPhase = "Starting"
		_, _, err := f.runCella("", "run", "--ephemeral", "--rm", "--", "true")
		if err == nil || !strings.Contains(err.Error(), "is still Starting") {
			t.Fatalf("err = %v", err)
		}
		if got := f.seen(); len(got) != 2 || !strings.HasPrefix(got[1], "DELETE ") {
			t.Errorf("requests = %v", got)
		}
	})
	t.Run("failed command", func(t *testing.T) {
		f := newFakeCore(t)
		f.exec = cellaclient.ExecResult{ExitCode: 3}
		_, _, err := f.runCella("", "run", "--ephemeral", "--rm", "--", "false")
		if code := exitCode(err); code != 3 {
			t.Fatalf("exit code = %d (%v), want 3", code, err)
		}
		if got := f.seen(); len(got) != 3 || !strings.HasPrefix(got[2], "DELETE ") {
			t.Errorf("requests = %v", got)
		}
	})
	t.Run("answer lost", func(t *testing.T) {
		f := newFakeCore(t)
		f.dropCreate = true
		_, _, err := f.runCella("", "run", "--ephemeral", "--rm", "--", "true")
		if err == nil {
			t.Fatal("run succeeded with no answer to its create")
		}
		got := f.seen()
		if len(got) != 2 || !strings.HasPrefix(got[1], "DELETE /sandboxes/run-") || strings.TrimPrefix(got[1], "DELETE ") != strings.Split(strings.TrimPrefix(got[0], "PUT "), "?")[0] {
			t.Errorf("requests = %v, want the name the create used deleted", got)
		}
	})
}

// A delete that fails is reported with the command to finish it, and the
// command's own exit code still wins.
func TestCellaRunEphemeralReportsFailedDelete(t *testing.T) {
	t.Run("command failed", func(t *testing.T) {
		f := newFakeCore(t)
		f.exec, f.deleteStatus = cellaclient.ExecResult{ExitCode: 3}, 500
		_, errOut, err := f.runCella("", "run", "--ephemeral", "--rm", "--", "false")
		if code := exitCode(err); code != 3 {
			t.Fatalf("exit code = %d (%v), want 3", code, err)
		}
		if !strings.Contains(errOut, "delete it with 'latere cella delete run-") {
			t.Errorf("stderr = %q, want the leftover cella named", errOut)
		}
	})
	t.Run("command succeeded", func(t *testing.T) {
		f := newFakeCore(t)
		f.deleteStatus = 500
		_, _, err := f.runCella("", "run", "--ephemeral", "--rm", "--", "true")
		if err == nil || !strings.Contains(err.Error(), "delete it with 'latere cella delete run-") || exitCode(err) != 1 {
			t.Fatalf("err = %v, want the failed delete as the result", err)
		}
	})
}

func TestCellaRunRequiresEphemeralAndRm(t *testing.T) {
	f := newFakeCore(t)
	for _, args := range [][]string{
		{"run", "dev", "--", "true"},
		{"run", "--ephemeral", "--", "true"},
	} {
		if _, _, err := f.runCella("", args...); err == nil || !strings.Contains(err.Error(), "'latere cella exec'") {
			t.Errorf("%v: err = %v, want the pointer to exec", args, err)
		}
	}
	for _, args := range [][]string{
		{"run", "--ephemeral", "--rm", "--timeout", "0", "--", "true"},
		{"run", "--ephemeral", "--rm", "--disk", "-1", "--", "true"},
		{"run", "--ephemeral", "--rm"},
	} {
		if _, _, err := f.runCella("", args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
	if got := f.seen(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}

func TestCellaLogs(t *testing.T) {
	f := newFakeCore(t)
	out, _, err := f.runCella("", "logs", "dev", "--follow", "--tail", "100", "--since", "2026-09-26T10:00:00Z")
	if err != nil || out != "main process output\n" {
		t.Fatalf("logs = %q, %v", out, err)
	}
	if got := f.last("GET", "/sandboxes/dev/logs").String(); got != "GET /sandboxes/dev/logs?follow=1&since=2026-09-26T10:00:00Z&tail=100" {
		t.Errorf("logs request = %s", got)
	}
	for _, args := range [][]string{
		{"logs", "dev", "--since", "yesterday"},
		{"logs", "dev", "--tail", "-1"},
	} {
		if _, _, err := f.runCella("", args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
	if _, _, err := f.runCella("", "logs", "dev", "cmd-1"); err == nil || !strings.Contains(err.Error(), "takes no command id") {
		t.Errorf("logs with a command id: %v", err)
	}
}
