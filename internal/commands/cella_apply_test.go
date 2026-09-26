// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const namedManifest = `apiVersion: cella.latere.ai/v1beta1
kind: Sandbox
metadata:
  name: dev
spec:
  image: base
`

const unnamedManifest = `{"apiVersion":"cella.latere.ai/v1beta1","kind":"Sandbox","spec":{"image":"gui"}}`

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sandbox.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A create answers at once with the sandbox Pending; the CLI prints it and
// says how to wait, and sends the manifest as written.
func TestCellaApplyAnswersPendingWithoutWait(t *testing.T) {
	f := newFakeCore(t)
	out, errOut, err := f.runCella("", "apply", "-f", writeManifest(t, unnamedManifest))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := f.seen(); !slices.Equal(got, []string{"POST /sandboxes"}) {
		t.Fatalf("requests = %v, want one unheld create", got)
	}
	req := f.last("POST", "/sandboxes")
	if string(req.Body) != unnamedManifest || req.ContentType != "application/json" {
		t.Errorf("create body %q as %q, want the manifest unchanged as JSON", req.Body, req.ContentType)
	}
	for _, want := range []string{"cella:", "generated", "phase:", "Pending", "sbx-generated", "cpu=2 memory=4Gi"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "generated is Pending") || !strings.Contains(errOut, "'apply --wait'") {
		t.Errorf("stderr = %q, want how to wait for the sandbox", errOut)
	}
}

// A manifest that names its sandbox is applied under that name, so applying
// it twice updates one sandbox; --wait holds the answer on the server.
func TestCellaApplyByNameHoldsWithWait(t *testing.T) {
	for _, tc := range []struct {
		flag, timeout string
	}{
		{"--wait", "10m0s"},
		{"--wait=2m", "2m0s"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			f := newFakeCore(t)
			out, errOut, err := f.runCella("", "apply", "-f", writeManifest(t, namedManifest), tc.flag)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			want := "PUT /sandboxes/dev?timeout=" + tc.timeout + "&wait=1"
			if got := f.seen(); !slices.Equal(got, []string{want}) {
				t.Fatalf("requests = %v, want %s", got, want)
			}
			if req := f.last("PUT", "/sandboxes/dev"); string(req.Body) != namedManifest || req.ContentType != "application/yaml" {
				t.Errorf("apply body %q as %q, want the manifest unchanged as YAML", req.Body, req.ContentType)
			}
			if !strings.Contains(out, "Running") || errOut != "" {
				t.Errorf("stdout=%q stderr=%q, want a running sandbox and no hint", out, errOut)
			}
		})
	}
}

// A held create whose sandbox failed prints it and exits 1 with its reason.
func TestCellaApplyWaitReportsFailure(t *testing.T) {
	f := newFakeCore(t)
	f.heldPhase, f.heldReason = cellaFailed, "ImagePullBackOff"
	out, _, err := f.runCella("", "apply", "-f", writeManifest(t, namedManifest), "--wait")
	if err == nil || !strings.Contains(err.Error(), "did not start") || !strings.Contains(err.Error(), "ImagePullBackOff") {
		t.Fatalf("err = %v, want the failure with its reason", err)
	}
	if exitCode(err) != 1 {
		t.Errorf("exit code = %d, want 1", exitCode(err))
	}
	if !strings.Contains(out, "Failed") || !strings.Contains(out, "reason:") {
		t.Errorf("output = %q, want the failed sandbox with its reason", out)
	}
}

// A hold that ends while the sandbox is still starting is no failure; the
// CLI says it has not run yet.
func TestCellaApplyWaitStillStarting(t *testing.T) {
	f := newFakeCore(t)
	f.heldPhase = cellaPending
	_, errOut, err := f.runCella("", "apply", "-f", writeManifest(t, namedManifest), "--wait=1s")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(errOut, "dev is still Pending after the hold") {
		t.Errorf("stderr = %q, want the sandbox named as still starting", errOut)
	}
}

func TestCellaApplyRefusesBadInputBeforeSending(t *testing.T) {
	f := newFakeCore(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"apply", "-f", ""}, "-f is required"},
		{[]string{"apply", "-f", writeManifest(t, namedManifest), "--wait=2h"}, "--wait must be positive and at most 1h"},
		{[]string{"apply", "-f", writeManifest(t, "  \n")}, "manifest is empty"},
	} {
		if _, _, err := f.runCella("", tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	if got := f.seen(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}

func TestCellaListFollowsPages(t *testing.T) {
	f := newFakeCore(t)
	f.listed, f.pageSize = []string{"dev", "ci", "gpu"}, 2
	out, _, err := f.runCella("", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := f.seen(); len(got) != 2 || got[1] != "GET /sandboxes?cursor=gpu&limit=200" {
		t.Fatalf("requests = %v, want the second page by cursor", got)
	}
	for _, name := range f.listed {
		if !strings.Contains(out, "sbx-"+name) {
			t.Errorf("output lacks %s:\n%s", name, out)
		}
	}

	out, _, err = f.runCella("", "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 3 {
		t.Fatalf("list --json = %q (%v), want three objects", out, err)
	}
	if items[0]["kind"] != "Sandbox" {
		t.Errorf("list --json item = %v, want the object as the core answered it", items[0])
	}
}

func TestCellaListEmpty(t *testing.T) {
	f := newFakeCore(t)
	out, _, err := f.runCella("", "list")
	if err != nil || !strings.Contains(out, "No cellas are visible") {
		t.Fatalf("list = %q, %v", out, err)
	}
	out, _, err = f.runCella("", "list", "--json")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("list --json = %q, %v, want an empty array", out, err)
	}
}

func TestCellaGetStartStopDelete(t *testing.T) {
	f := newFakeCore(t)
	out, _, err := f.runCella("", "get", "dev")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil || obj["kind"] != "Sandbox" {
		t.Fatalf("get = %q (%v), want the object as JSON", out, err)
	}
	if !strings.HasPrefix(out, "{\n  ") {
		t.Errorf("get output is not indented: %q", out)
	}
	if out, _, err = f.runCella("", "stop", "dev"); err != nil || !strings.Contains(out, "Stopped") {
		t.Fatalf("stop = %q, %v", out, err)
	}
	if out, _, err = f.runCella("", "start", "dev"); err != nil || !strings.Contains(out, "Running") {
		t.Fatalf("start = %q, %v", out, err)
	}
	_, errOut, err := f.runCella("", "delete", "dev")
	if err != nil || !strings.Contains(errOut, "deleted dev") {
		t.Fatalf("delete = %q, %v", errOut, err)
	}
	want := []string{"GET /sandboxes/dev", "POST /sandboxes/dev/stop", "POST /sandboxes/dev/start", "DELETE /sandboxes/dev"}
	if got := f.seen(); !slices.Equal(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	for _, r := range f.all() {
		if r.Auth != "Bearer "+coreToken {
			t.Errorf("%s carried %q", r, r.Auth)
		}
	}
}

func TestCellaRefusalReachesTheUser(t *testing.T) {
	f := newFakeCore(t)
	f.deleteStatus = 409
	_, _, err := f.runCella("", "delete", "dev")
	if err == nil || err.Error() != "The delete failed." {
		t.Fatalf("err = %v, want the control plane's sentence", err)
	}
}
