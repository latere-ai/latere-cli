// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	v1 "latere.ai/x/cella/manifest/v1"
)

// refusal is one error envelope the fake core answers with.
type refusal struct {
	status        int
	code, message string
}

// The value the secret tests write. Every test that sends one checks it is
// in no output and no error.
const testSecretValue = "ghp_0123456789abcdefSECRET"

// serveSecrets is the core's Secret routes under /secrets: a create that
// refuses a name in use, an apply that creates or replaces, reads by name
// or id, and a delete that answers the deleted object. No answer carries a
// value, as the core's never does.
func (f *fakeCore) serveSecrets(w http.ResponseWriter, r *http.Request, parts []string, body []byte) {
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		f.mu.Lock()
		items := []any{}
		for _, name := range slices.Sorted(maps.Keys(f.secrets)) {
			items = append(items, f.secrets[name])
		}
		f.mu.Unlock()
		f.answer(w, http.StatusOK, map[string]any{"items": items, "next": ""})
	case len(parts) == 0 && r.Method == http.MethodPost:
		f.writeSecret(w, "", body)
	case len(parts) == 1 && r.Method == http.MethodPut:
		f.writeSecret(w, parts[0], body)
	case len(parts) == 1 && (r.Method == http.MethodGet || r.Method == http.MethodDelete):
		f.mu.Lock()
		defer f.mu.Unlock()
		for name, obj := range f.secrets {
			if name != parts[0] && obj["status"].(map[string]any)["id"] != parts[0] {
				continue
			}
			if r.Method == http.MethodDelete {
				delete(f.secrets, name)
			}
			f.answer(w, http.StatusOK, obj)
			return
		}
		f.refuse(w, http.StatusNotFound, "not_found", "The secret does not exist.")
	default:
		f.refuse(w, http.StatusNotFound, "not_found", "No such route.")
	}
}

// writeSecret is a create (key empty) or an apply under key: the spec is
// stored as written less its value, and an apply over a stored secret keeps
// its status and counts a version.
func (f *fakeCore) writeSecret(w http.ResponseWriter, key string, body []byte) {
	if r := f.secretRefusal; r != nil {
		f.refuse(w, r.status, r.code, r.message)
		return
	}
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil || doc == nil {
		f.refuse(w, http.StatusBadRequest, "invalid_manifest", "The manifest does not read.")
		return
	}
	metadata, _ := doc["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	if name == "" {
		name = key
	}
	if name == "" {
		f.refuse(w, http.StatusBadRequest, "missing_field", "A secret is named by the caller.")
		return
	}
	spec, _ := doc["spec"].(map[string]any)
	delete(spec, "value")
	f.mu.Lock()
	defer f.mu.Unlock()
	held, exists := f.secrets[name]
	if exists && key == "" {
		f.refuse(w, http.StatusConflict, "name_taken", "You already have an object with this name.")
		return
	}
	status, code := map[string]any{
		"id": "sec_" + name, "owner": "https://issuer.example.com|alice", "version": 1,
		"createdAt": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "mountedBy": 0,
	}, http.StatusCreated
	if exists {
		status, code = maps.Clone(held["status"].(map[string]any)), http.StatusOK
		status["version"] = status["version"].(int) + 1
		status["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
	}
	obj := map[string]any{"apiVersion": doc["apiVersion"], "kind": doc["kind"], "metadata": metadata, "spec": spec, "status": status}
	f.secrets[name] = obj
	f.answer(w, code, obj)
}

// seedSecret stores a secret as the core recorded it: the account's labels,
// the spec with its defaults filled, and mounted workloads attaching it.
func (f *fakeCore) seedSecret(name string, spec map[string]any, mounted int) {
	labels := map[string]any{}
	for k, v := range fakeLabels {
		labels[k] = v
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[name] = map[string]any{
		"apiVersion": v1.APIVersion, "kind": v1.KindSecret,
		"metadata": map[string]any{"name": name, "labels": labels},
		"spec":     spec,
		"status": map[string]any{
			"id": "sec_" + name, "owner": "https://issuer.example.com|alice", "version": 1,
			"createdAt": time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339), "mountedBy": mounted,
		},
	}
}

// bearerSpec is a bearer secret's spec as the core stores it.
func bearerSpec() map[string]any {
	return map[string]any{
		"kind":   "static",
		"scope":  map[string]any{"hosts": []any{"api.github.com"}, "ports": []any{443}},
		"inject": map[string]any{"header": "Authorization", "scheme": "bearer"},
	}
}

// sentSecret is the Secret a write carried, decoded as the core decodes it.
func sentSecret(t *testing.T, req coreRequest) v1.Secret {
	t.Helper()
	var s v1.Secret
	if err := json.Unmarshal(req.Body, &s); err != nil {
		t.Fatalf("the write's body is not a Secret: %v\n%s", err, req.Body)
	}
	return s
}

// A create made from flags carries the account's labels, which the platform
// refuses a create without, and the value read from standard input without
// its line ending. No output repeats the value.
func TestSecretsCreateCarriesLabelsAndTheValueFromStdin(t *testing.T) {
	f := newFakeCore(t)
	out, errOut, err := f.runCella(testSecretValue+"\n", "secrets", "create", "github-token", "--host", "api.github.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := f.seen(); !slices.Equal(got, []string{"POST /secrets"}) {
		t.Fatalf("requests = %v, want one create", got)
	}
	req := f.last("POST", "/secrets")
	if req.ContentType != "application/json" || req.Auth != "Bearer "+coreToken {
		t.Errorf("create sent as %q with %q", req.ContentType, req.Auth)
	}
	sent := sentSecret(t, req)
	if !maps.Equal(sent.Metadata.Labels, fakeLabels) {
		t.Errorf("labels = %v, want the account's", sent.Metadata.Labels)
	}
	if got := f.reads(); !slices.Equal(got, []string{"Bearer " + platformToken}) {
		t.Errorf("labels reads = %v, want one with the platform's bearer", got)
	}
	if sent.APIVersion != v1.APIVersion || sent.Kind != v1.KindSecret || sent.Metadata.Name != "github-token" {
		t.Errorf("sent %s %s %q", sent.APIVersion, sent.Kind, sent.Metadata.Name)
	}
	if sent.Spec.Value != testSecretValue {
		t.Errorf("value sent = %q, want the line read without its ending", sent.Spec.Value)
	}
	for _, want := range []string{"secret:", "github-token", "sec_github-token", "as a bearer token in the Authorization header", "api.github.com", "to no workload"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	nowhere(t, testSecretValue, err, out, errOut)
}

// Each --type is the spec the console's Add a secret writes for the same
// choice, and a two-part value joins its flag's half to the one read.
func TestSecretsCreateMapsEachTypeAsTheConsole(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		wantSpec  v1.SecretSpec
		wantValue string
	}{
		{
			name: "bearer",
			args: []string{"--host", "api.github.com"},
			wantSpec: v1.SecretSpec{Kind: "static", Scope: v1.SecretScope{Hosts: []string{"api.github.com"}},
				Inject: v1.SecretInject{Header: "Authorization", Scheme: "bearer"}},
			wantValue: testSecretValue,
		},
		{
			name: "header",
			args: []string{"--type", "header", "--header", "X-Api-Key", "--host", "a.example.com,b.example.com", "--port", "443", "--port", "8443", "--body"},
			wantSpec: v1.SecretSpec{Kind: "static", Scope: v1.SecretScope{Hosts: []string{"a.example.com", "b.example.com"}, Ports: []int{443, 8443}},
				Inject: v1.SecretInject{Header: "X-Api-Key", Scheme: "raw", Body: true}},
			wantValue: testSecretValue,
		},
		{
			name: "basic",
			args: []string{"--type", "basic", "--username", "ci", "--host", "registry.example.com"},
			wantSpec: v1.SecretSpec{Kind: "static", Scope: v1.SecretScope{Hosts: []string{"registry.example.com"}},
				Inject: v1.SecretInject{Header: "Authorization", Scheme: "basic"}},
			wantValue: "ci:" + testSecretValue,
		},
		{
			name: "query",
			args: []string{"--type", "query", "--query", "key", "--host", "maps.example.com"},
			wantSpec: v1.SecretSpec{Kind: "static", Scope: v1.SecretScope{Hosts: []string{"maps.example.com"}},
				Inject: v1.SecretInject{Query: "key"}},
			wantValue: testSecretValue,
		},
		{
			name: "oauth",
			args: []string{"--type", "oauth", "--client-id", "app-1", "--token-url", "https://auth.example.com/oauth/token",
				"--token-scope", "read write", "--token-audience", "https://api.example.com", "--host", "api.example.com"},
			wantSpec: v1.SecretSpec{Kind: "oauth_client_credentials", Scope: v1.SecretScope{Hosts: []string{"api.example.com"}},
				Inject: v1.SecretInject{Header: "Authorization", Scheme: "bearer"},
				OAuth:  &v1.SecretOAuth{TokenURL: "https://auth.example.com/oauth/token", Scope: "read write", Audience: "https://api.example.com"}},
			wantValue: "app-1:" + testSecretValue,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCore(t)
			args := append([]string{"secrets", "create", "s1"}, tc.args...)
			out, errOut, err := f.runCella(testSecretValue, args...)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			sent := sentSecret(t, f.last("POST", "/secrets"))
			if sent.Spec.Value != tc.wantValue {
				t.Errorf("value = %q, want %q", sent.Spec.Value, tc.wantValue)
			}
			sent.Spec.Value = ""
			if !reflect.DeepEqual(sent.Spec, tc.wantSpec) {
				t.Errorf("spec = %+v, want %+v", sent.Spec, tc.wantSpec)
			}
			nowhere(t, testSecretValue, err, out, errOut)
		})
	}
}

// At a terminal the value is typed at a prompt on stderr, which shows the
// prompt and never the value.
func TestSecretsCreateReadsTheValueAtAPrompt(t *testing.T) {
	typedAtATerminal(t, testSecretValue)
	f := newFakeCore(t)
	out, errOut, err := f.runCella("", "secrets", "create", "github-token", "--host", "api.github.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := sentSecret(t, f.last("POST", "/secrets")).Spec.Value; got != testSecretValue {
		t.Errorf("value sent = %q, want the one typed", got)
	}
	if !strings.Contains(errOut, "Token for github-token: ") {
		t.Errorf("stderr = %q, want the prompt", errOut)
	}
	nowhere(t, testSecretValue, err, out, errOut)
}

// A create the flags cannot make, or with no value to send, is refused
// before any request, and a value pasted as an argument is not repeated.
func TestSecretsCreateRefusesBeforeSending(t *testing.T) {
	f := newFakeCore(t)
	for _, tc := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{testSecretValue, []string{"s1"}, "--host is required"},
		{testSecretValue, []string{"s1", "--host", " , "}, "--host is required"},
		{testSecretValue, []string{"s1", "--type", "digest", "--host", "h.example.com"}, "--type is one of bearer, header, basic, query, oauth"},
		{testSecretValue, []string{"s1", "--type", "header", "--host", "h.example.com"}, "--type header needs --header"},
		{testSecretValue, []string{"s1", "--type", "query", "--host", "h.example.com"}, "--type query needs --query"},
		{testSecretValue, []string{"s1", "--type", "oauth", "--client-id", "a", "--host", "h.example.com"}, "--type oauth needs --token-url"},
		{testSecretValue, []string{"s1", "--type", "oauth", "--token-url", "https://a.example.com/t", "--host", "h.example.com"}, "an oauth secret needs --client-id"},
		{testSecretValue, []string{"s1", "--header", "X-Key", "--host", "h.example.com"}, "--header belongs to --type header"},
		{testSecretValue, []string{"s1", "--token-url", "https://a.example.com/t", "--host", "h.example.com"}, "--token-url belongs to --type oauth"},
		{testSecretValue, []string{"s1", "--username", "ci", "--host", "h.example.com"}, "--username belongs to a basic secret"},
		{testSecretValue, []string{"s1", "--type", "basic", "--host", "h.example.com"}, "a basic secret needs --username"},
		{testSecretValue, []string{"s1", "--type", "basic", "--username", "c:i", "--host", "h.example.com"}, "--username cannot contain a colon"},
		{"", []string{"s1", "--host", "h.example.com"}, "standard input held no token"},
		{"\n", []string{"s1", "--host", "h.example.com"}, "standard input held no token"},
		{strings.Repeat("x", 64<<10+1), []string{"s1", "--host", "h.example.com"}, "longer than 65536 bytes"},
		{"", []string{"s1", testSecretValue, "--host", "h.example.com"}, "takes the secret's name alone"},
		{"", []string{"s1", "--port", testSecretValue, "--host", "h.example.com"}, "is not repeated here in case it holds a value"},
	} {
		args := append([]string{"secrets", "create"}, tc.args...)
		_, errOut, err := f.runCella(tc.stdin, args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
		nowhere(t, testSecretValue, err, errOut)
	}
	if got := f.seen(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
	if got := f.reads(); len(got) != 0 {
		t.Errorf("labels reads = %v, want none", got)
	}
}

// A create the core refuses ends with the core's own sentence, and the
// value is in neither the error nor any output.
func TestSecretsCreateRefusedSurfacesTheServersMessage(t *testing.T) {
	f := newFakeCore(t)
	f.secretRefusal = &refusal{http.StatusForbidden, "forbidden", "Your account may not create secrets here."}
	out, errOut, err := f.runCella(testSecretValue, "secrets", "create", "github-token", "--host", "api.github.com")
	if err == nil || err.Error() != "Your account may not create secrets here." {
		t.Fatalf("err = %v, want the core's sentence", err)
	}
	if exitCode(err) != 1 {
		t.Errorf("exit code = %d, want 1", exitCode(err))
	}
	nowhere(t, testSecretValue, err, out, errOut)
}

// A name in use is refused, never replaced, and the refusal says what
// replaces a value.
func TestSecretsCreateNeverReplacesAStoredSecret(t *testing.T) {
	f := newFakeCore(t)
	f.seedSecret("github-token", bearerSpec(), 0)
	_, errOut, err := f.runCella(testSecretValue, "secrets", "create", "github-token", "--host", "api.github.com")
	if err == nil || !strings.HasPrefix(err.Error(), "You already have an object with this name.") || !strings.Contains(err.Error(), "'latere environments secrets rotate github-token'") {
		t.Fatalf("err = %v, want the core's sentence and the rotate command", err)
	}
	if got := f.seen(); !slices.Equal(got, []string{"POST /secrets"}) {
		t.Errorf("requests = %v, want the create alone", got)
	}
	nowhere(t, testSecretValue, err, errOut)
}

// A rotation sends back the secret as read, its labels and spec kept and
// its status left out, with the new value; the platform admits an update
// only with the stored labels, and PUT replaces the whole object.
func TestSecretsRotateKeepsEverythingButTheValue(t *testing.T) {
	f := newFakeCore(t)
	f.seedSecret("github-token", bearerSpec(), 2)
	out, errOut, err := f.runCella(testSecretValue+"\r\n", "secrets", "rotate", "sec_github-token")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	want := []string{"GET /secrets/sec_github-token", "PUT /secrets/github-token"}
	if got := f.seen(); !slices.Equal(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	put := f.last("PUT", "/secrets/github-token")
	var body map[string]any
	if err := json.Unmarshal(put.Body, &body); err != nil {
		t.Fatalf("rotation body: %v", err)
	}
	if _, ok := body["status"]; ok {
		t.Errorf("rotation body carries the status: %s", put.Body)
	}
	sent := sentSecret(t, put)
	if !maps.Equal(sent.Metadata.Labels, fakeLabels) || sent.Metadata.Name != "github-token" {
		t.Errorf("metadata = %+v, want the stored name and labels", sent.Metadata)
	}
	if sent.Spec.Value != testSecretValue {
		t.Errorf("value = %q, want the new one without its line ending", sent.Spec.Value)
	}
	sent.Spec.Value = ""
	wantSpec := v1.SecretSpec{Kind: "static", Scope: v1.SecretScope{Hosts: []string{"api.github.com"}, Ports: []int{443}},
		Inject: v1.SecretInject{Header: "Authorization", Scheme: "bearer"}}
	if !reflect.DeepEqual(sent.Spec, wantSpec) {
		t.Errorf("spec = %+v, want the stored one", sent.Spec)
	}
	if !strings.Contains(out, "version:    2") || !strings.Contains(out, "to 2 workloads") {
		t.Errorf("output = %q, want version 2 attached to 2 workloads", out)
	}
	if !strings.Contains(errOut, "rotated github-token") {
		t.Errorf("stderr = %q, want the rotation named", errOut)
	}
	nowhere(t, testSecretValue, err, out, errOut)
}

// A basic secret's value is the pair, so a rotation takes the username
// again, and a flag of another type is refused before the value is read.
func TestSecretsRotateTwoPartValues(t *testing.T) {
	f := newFakeCore(t)
	f.seedSecret("registry", map[string]any{
		"kind":   "static",
		"scope":  map[string]any{"hosts": []any{"registry.example.com"}, "ports": []any{443}},
		"inject": map[string]any{"header": "Authorization", "scheme": "basic"},
	}, 0)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"registry"}, "a basic secret needs --username"},
		{[]string{"registry", "--username", "ci", "--client-id", "app"}, "--client-id belongs to an oauth secret"},
	} {
		_, _, err := f.runCella(testSecretValue, append([]string{"secrets", "rotate"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	out, errOut, err := f.runCella(testSecretValue, "secrets", "rotate", "registry", "--username", "ci")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if got := sentSecret(t, f.last("PUT", "/secrets/registry")).Spec.Value; got != "ci:"+testSecretValue {
		t.Errorf("value = %q, want the username and the password", got)
	}
	nowhere(t, testSecretValue, err, out, errOut)
	if got := f.seen(); slices.Index(got, "PUT /secrets/registry") != len(got)-1 {
		t.Errorf("requests = %v, want one rotation, the last", got)
	}
}

// apply sends a Secret manifest as written with the account's labels, under
// the name it declares, and prints the answer without the value. A manifest
// of the other kind is pointed at its own command before anything is sent.
func TestSecretsApply(t *testing.T) {
	manifest := "apiVersion: cella.latere.ai/v1beta1\nkind: Secret\nmetadata:\n  name: github-token # read by the workload\nspec:\n  scope:\n    hosts: [api.github.com]\n  value: " + testSecretValue + "\n"
	f := newFakeCore(t)
	out, errOut, err := f.runCella("", "secrets", "apply", "-f", writeManifest(t, manifest))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	put := f.last("PUT", "/secrets/github-token")
	if put.ContentType != "application/yaml" || !strings.Contains(string(put.Body), "# read by the workload") {
		t.Errorf("apply body %q as %q, want the manifest's YAML with its comments", put.Body, put.ContentType)
	}
	if got := labelsOf(t, put.Body); !maps.Equal(got, fakeLabels) {
		t.Errorf("labels = %v, want the account's", got)
	}
	if !strings.Contains(out, "github-token") {
		t.Errorf("output = %q, want the secret", out)
	}
	nowhere(t, testSecretValue, err, out, errOut)

	if _, _, err := f.runCella("", "secrets", "apply", "-f", writeManifest(t, namedManifest)); err == nil || !strings.Contains(err.Error(), "'latere environments apply -f'") {
		t.Errorf("secrets apply of a workload: err = %v, want the workload command", err)
	}
	if _, _, err := f.runCella("", "apply", "-f", writeManifest(t, manifest)); err == nil || !strings.Contains(err.Error(), "'latere environments secrets apply -f'") {
		t.Errorf("apply of a Secret: err = %v, want the secrets command", err)
	}
	if got := f.seen(); !slices.Equal(got, []string{"PUT /secrets/github-token"}) {
		t.Errorf("requests = %v, want the one apply", got)
	}
}

// delete removes the secret and says how many workloads still attached it.
func TestSecretsDelete(t *testing.T) {
	f := newFakeCore(t)
	f.seedSecret("github-token", bearerSpec(), 2)
	out, errOut, err := f.runCella("", "secrets", "delete", "github-token")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := f.seen(); !slices.Equal(got, []string{"DELETE /secrets/github-token"}) {
		t.Fatalf("requests = %v", got)
	}
	if out != "" || !strings.Contains(errOut, "deleted secret github-token") || !strings.Contains(errOut, "2 workloads attached it") {
		t.Errorf("stdout=%q stderr=%q, want the deletion and the workloads that attached it", out, errOut)
	}
	if _, _, err := f.runCella("", "secrets", "delete", "github-token"); err == nil || err.Error() != "The secret does not exist." {
		t.Errorf("second delete: err = %v, want the core's not found", err)
	}
}

// list prints a record per secret, or the API's objects with --json, and get
// prints one object as JSON.
func TestSecretsListAndGet(t *testing.T) {
	f := newFakeCore(t)
	out, _, err := f.runCella("", "secrets", "list")
	if err != nil || !strings.Contains(out, "No secrets are visible") {
		t.Fatalf("empty list = %q, %v", out, err)
	}
	f.seedSecret("github-token", bearerSpec(), 1)
	f.seedSecret("search", map[string]any{
		"kind":   "static",
		"scope":  map[string]any{"hosts": []any{"api.example.com"}, "ports": []any{443}},
		"inject": map[string]any{"query": "key", "scheme": "raw", "body": true},
	}, 0)
	out, _, err = f.runCella("", "secrets", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"github-token", "to 1 workload\n", "search", "in the key query parameter, and in the request body", "created:"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	out, _, err = f.runCella("", "secrets", "list", "--json")
	var items []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &items) != nil || len(items) != 2 || items[0]["kind"] != "Secret" {
		t.Fatalf("list --json = %q, %v, want two Secrets", out, err)
	}
	out, _, err = f.runCella("", "secrets", "get", "search")
	var obj map[string]any
	if err != nil || json.Unmarshal([]byte(out), &obj) != nil || obj["kind"] != "Secret" || !strings.HasPrefix(out, "{\n  ") {
		t.Fatalf("get = %q, %v, want the object as indented JSON", out, err)
	}
}

// Each stored spec reads back as the --type that writes it, and is
// described by where its value goes.
func TestSecretSentence(t *testing.T) {
	for _, tc := range []struct {
		spec v1.SecretSpec
		typ  string
		want string
	}{
		{v1.SecretSpec{}, secretBearer, "as a bearer token in the Authorization header"},
		{v1.SecretSpec{Inject: v1.SecretInject{Header: "authorization", Scheme: "bearer"}}, secretBearer, "as a bearer token in the authorization header"},
		{v1.SecretSpec{Inject: v1.SecretInject{Header: "Authorization", Scheme: "basic"}}, secretBasic, "as basic credentials in the Authorization header"},
		{v1.SecretSpec{Inject: v1.SecretInject{Header: "X-Api-Key", Scheme: "raw", Body: true}}, secretHeader, "in the X-Api-Key header, and in the request body"},
		{v1.SecretSpec{Inject: v1.SecretInject{Header: "Authorization", Scheme: "raw"}}, secretHeader, "in the Authorization header"},
		{v1.SecretSpec{Inject: v1.SecretInject{Query: "key"}}, secretQuery, "in the key query parameter"},
		{v1.SecretSpec{Kind: v1.SecretOAuthClientCredentials, OAuth: &v1.SecretOAuth{TokenURL: "https://a.example.com/t"}}, secretOAuth, "as an access token from https://a.example.com/t, in the Authorization header"},
		{v1.SecretSpec{Kind: v1.SecretOAuthClientCredentials}, secretOAuth, "as an access token from the token endpoint, in the Authorization header"},
	} {
		if got := secretTypeOf(tc.spec); got != tc.typ {
			t.Errorf("%+v: type %q, want %q", tc.spec, got, tc.typ)
		}
		if got := secretSentence(tc.spec); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.spec, got, tc.want)
		}
	}
}

// The group's help is in the product's words: workloads, not sandboxes, and
// no dash where a sentence takes a period or a comma.
func TestSecretsHelpWords(t *testing.T) {
	group := newCeSecretsCmd()
	for _, c := range append([]*cobra.Command{group}, group.Commands()...) {
		for _, text := range []string{c.Short, c.Long, c.Example} {
			if strings.Contains(strings.ToLower(text), "sandbox") || strings.ContainsRune(text, '\u2014') {
				t.Errorf("%s help: %q", c.CommandPath(), text)
			}
		}
	}
}
