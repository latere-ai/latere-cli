// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// labelsOf is the metadata.labels of a create's body, read as the control
// plane reads it: YAML, of which JSON is a subset, with every value a string.
func labelsOf(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var m struct {
		Metadata struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(body, &m); err != nil {
		t.Fatalf("the create's body does not read: %v\n%s", err, body)
	}
	return m.Metadata.Labels
}

// withLabels is fakeLabels and extra.
func withLabels(extra map[string]string) map[string]string {
	out := maps.Clone(fakeLabels)
	maps.Copy(out, extra)
	return out
}

// The platform admits a create only when the object carries the caller's
// tenant, principal and organization labels, so every create the CLI makes
// carries the labels the platform answers for the login: apply in YAML, in
// JSON, and the disposable workload of run. The labels are read with a
// bearer for the platform, never the Environments one.
func TestEveryCreateCarriesTheAccountsLabels(t *testing.T) {
	t.Run("apply YAML", func(t *testing.T) {
		f := newFakeCore(t)
		manifest := "apiVersion: cella.latere.ai/v1beta1\nkind: Sandbox\nmetadata:\n  name: dev # the name the list shows\n  labels:\n    team: research\nspec:\n  image: base\n"
		if _, _, err := f.runCella("", "apply", "-f", writeManifest(t, manifest)); err != nil {
			t.Fatalf("apply: %v", err)
		}
		req := f.last("PUT", "/sandboxes/dev")
		if got := labelsOf(t, req.Body); !maps.Equal(got, withLabels(map[string]string{"team": "research"})) {
			t.Errorf("labels = %v, want the account's beside the manifest's own", got)
		}
		if req.ContentType != "application/yaml" || !strings.Contains(string(req.Body), "# the name the list shows") {
			t.Errorf("body %q as %q, want the manifest's YAML with its comments", req.Body, req.ContentType)
		}
		if got := f.reads(); !slices.Equal(got, []string{"Bearer " + platformToken}) {
			t.Errorf("labels reads = %v, want one with the platform's bearer", got)
		}
	})
	t.Run("apply JSON", func(t *testing.T) {
		f := newFakeCore(t)
		if _, _, err := f.runCella("", "apply", "-f", writeManifest(t, unnamedManifest)); err != nil {
			t.Fatalf("apply: %v", err)
		}
		req := f.last("POST", "/sandboxes")
		var body map[string]any
		if err := json.Unmarshal(req.Body, &body); err != nil || req.ContentType != "application/json" {
			t.Fatalf("body %q as %q, want JSON: %v", req.Body, req.ContentType, err)
		}
		if got := labelsOf(t, req.Body); !maps.Equal(got, fakeLabels) {
			t.Errorf("labels = %v, want the account's", got)
		}
		if spec, _ := body["spec"].(map[string]any); spec["image"] != "gui" {
			t.Errorf("body = %s, want the manifest's spec kept", req.Body)
		}
	})
	t.Run("run", func(t *testing.T) {
		f := newFakeCore(t)
		if _, _, err := f.runCella("", "run", "--ephemeral", "--rm", "--", "true"); err != nil {
			t.Fatalf("run: %v", err)
		}
		reqs := f.all()
		if len(reqs) != 3 || reqs[0].Method != http.MethodPut {
			t.Fatalf("requests = %v, want create, exec, delete", f.seen())
		}
		if got := labelsOf(t, reqs[0].Body); !maps.Equal(got, fakeLabels) {
			t.Errorf("labels = %v, want the account's", got)
		}
	})
}

// A platform label that names another account is replaced by the login's,
// as the console replaces it: the platform refuses every other value. A
// manifest that already carries the login's labels is sent as written.
func TestTheAccountsLabelsReplaceAnotherAccountsAndKeepTheirOwn(t *testing.T) {
	f := newFakeCore(t)
	other := "metadata:\n  name: dev\n  labels:\n    platform.latere.ai/tenant: p-ffffffffffffffffffffffffffffffff\n"
	if _, _, err := f.runCella("", "apply", "-f", writeManifest(t, other)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := labelsOf(t, f.last("PUT", "/sandboxes/dev").Body); !maps.Equal(got, fakeLabels) {
		t.Errorf("labels = %v, want the login's in place of another account's", got)
	}

	carrying := "metadata:\n  name: dev\n  labels:\n    platform.latere.ai/tenant: p-0123456789abcdef0123456789abcdef\n    platform.latere.ai/principal: \"0042\"\n"
	if _, _, err := f.runCella("", "apply", "-f", writeManifest(t, carrying)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := string(f.last("PUT", "/sandboxes/dev").Body); got != carrying {
		t.Errorf("body = %q, want a manifest that carries the labels sent as written", got)
	}
}

// A manifest the CLI cannot read is sent as written, so the refusal the
// person reads is the control plane's, with its field.
func TestAnUnreadableManifestIsSentAsWritten(t *testing.T) {
	for _, body := range []string{"metadata: plain\n", "{not json", "spec: [\n", "- a\n", "metadata:\n  labels: [a]\n", "kind: Sandbox\n---\nkind: Sandbox\n"} {
		got, err := stampTenantLabels([]byte(body), fakeLabels)
		if err != nil || string(got) != body {
			t.Errorf("stamp(%q) = %q, %v; want it unchanged", body, got, err)
		}
	}
}

// Without the labels no create can succeed, so a labels read the platform
// refuses stops the command before anything is created, and says how to
// sign in again.
func TestNoCreateWithoutTheAccountsLabels(t *testing.T) {
	for _, args := range [][]string{
		{"apply", "-f", ""},
		{"run", "--ephemeral", "--rm", "--", "true"},
	} {
		t.Run(args[0], func(t *testing.T) {
			f := newFakeCore(t)
			f.labelsStatus = http.StatusUnauthorized
			if args[0] == "apply" {
				args[2] = writeManifest(t, namedManifest)
			}
			_, _, err := f.runCella("", args...)
			if err == nil || !strings.Contains(err.Error(), "your account's labels") || !strings.Contains(err.Error(), "latere login") {
				t.Fatalf("err = %v, want the labels read's refusal and how to sign in", err)
			}
			if got := f.seen(); len(got) != 0 {
				t.Errorf("requests = %v, want no create", got)
			}
		})
	}
}
