// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	cellaclient "latere.ai/x/cella/client"
	"latere.ai/x/cella/manifest"
	v1 "latere.ai/x/cella/manifest/v1"
)

// The ways a secret is sent, as --type names them: the five the console's
// Add a secret offers, each mapped onto the core's kind, inject and oauth
// fields as the console maps them.
const (
	secretBearer = "bearer"
	secretHeader = "header"
	secretBasic  = "basic"
	secretQuery  = "query"
	secretOAuth  = "oauth"
)

// secretTypes is every --type, in the order the help lists them.
var secretTypes = []string{secretBearer, secretHeader, secretBasic, secretQuery, secretOAuth}

// secretTypeList is the help's description of each --type.
const secretTypeList = `  bearer   Authorization: Bearer <value>. The default.
  header   The value in the header --header names, such as X-Api-Key.
  basic    Authorization: Basic, from --username and a password.
  query    The value in the query parameter --query names, such as key.
  oauth    Authorization: Bearer with an access token the gateway requests
           at --token-url with --client-id and a client secret.`

// labelOrg is the label a create in an organization carries. A secret that
// carries it is the organization's: its owners and admins may attach it.
const labelOrg = "platform.latere.ai/org"

// newCeSecretsCmd is the `latere environments secrets …` command tree: the
// core's Secret kind, which a workload attaches by name in its manifest.
func newCeSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage secrets: credentials a workload uses without holding them.",
		Long: `Manage the secrets of Latere Environments.

A secret is a credential a workload uses without holding it. The workload
reads a placeholder from an environment variable, and the egress gateway
puts the value in the placeholder's place on requests to the secret's hosts
and ports, and nowhere else. No command prints a value, and the API never
returns one after it is saved.

'create --type' names how the service takes the credential:

` + secretTypeList + `

A workload attaches a secret in its manifest, under the environment
variable it reads the placeholder from:

  spec:
    secrets:
      - {name: github-token, env: GITHUB_TOKEN}

In an organization, a secret belongs to the organization: its owners and
admins can attach it to their workloads, and no one can read its value.`,
		Example: `  printf %s "$GITHUB_TOKEN" | latere environments secrets create github-token --host api.github.com
  latere environments secrets create search --type header --header X-Api-Key --host api.example.com
  latere environments secrets list
  latere environments secrets rotate github-token
  latere environments secrets delete github-token`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newCeSecretCreateCmd(),
		newCeSecretRotateCmd(),
		newCeSecretApplyCmd(),
		newCeSecretListCmd(),
		newCeSecretGetCmd(),
		newCeSecretDeleteCmd(),
	)
	return cmd
}

// ---- create / rotate ----

// secretFlags are the flags of `secrets create` that make a secret's spec.
// The value is none of them: it is read from standard input or a prompt.
type secretFlags struct {
	typ, header, query                  string
	tokenURL, tokenScope, tokenAudience string
	username, clientID                  string
	hosts                               []string
	ports                               []int
	body                                bool
}

func newCeSecretCreateCmd() *cobra.Command {
	var (
		apiURL string
		jsonF  bool
		s      secretFlags
	)
	cmd := &cobra.Command{
		Use:   "create <name> --host <host>",
		Short: "Create a secret, with its value from standard input or a prompt.",
		Long: `Create a secret named <name>, sent only to the hosts --host names.

The value is never a flag or an argument, which a shell history would
keep. When standard input is a terminal, it is typed at a prompt without
being shown; otherwise standard input is read whole, with one trailing line
ending removed. For basic and oauth the value is the password or the client
secret, and --username or --client-id is the other half.

--type names how the service takes the credential:

` + secretTypeList + `

--host names a host the value may be sent to, such as api.github.com or
*.example.com; repeat it or separate hosts with commas. There is no setting
that sends a secret to every host. --port names the ports on those hosts,
` + strconv.Itoa(v1.DefaultSecretPort) + ` unless given. --body also puts the value in JSON, form and text
request bodies; leave it off unless the service reads the credential from
the body.

The CLI adds your account's labels for your current context, as
'latere environments apply' does. A name already in use is refused:
'rotate' replaces a secret's value, and 'apply -f' changes the rest.`,
		Example: `  printf %s "$GITHUB_TOKEN" | latere environments secrets create github-token --host api.github.com
  latere environments secrets create search --type header --header X-Api-Key --host api.example.com
  latere environments secrets create maps --type query --query key --host maps.example.com
  latere environments secrets create registry --type basic --username ci --host registry.example.com
  latere environments secrets create billing --type oauth --client-id app-1 \
      --token-url https://auth.example.com/oauth/token --host api.example.com`,
		Args: secretRefArg("the secret's name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			spec, err := s.spec()
			if err != nil {
				return err
			}
			half, err := pairHalf(s.typ, s.username, s.clientID)
			if err != nil {
				return err
			}
			// The value is asked for before any request. A terminal shows
			// what is typed until the prompt's read turns that off, so a
			// prompt that waited for the network would show whatever a person
			// typed ahead of it.
			value, err := readSecretValue(cmd.InOrStdin(), cmd.ErrOrStderr(), secretValueLabel(s.typ), name)
			if err != nil {
				return err
			}
			spec.Value = joinSecretValue(half, value)
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			labels, err := tenantLabels(cmd.Context(), "")
			if err != nil {
				return err
			}
			m, err := cellaclient.Encode(v1.Secret{
				APIVersion: v1.APIVersion,
				Kind:       v1.KindSecret,
				Metadata:   v1.Metadata{Name: name, Labels: labels},
				Spec:       spec,
			})
			if err != nil {
				return fmt.Errorf("encode the secret: %w", err)
			}
			// A create, never an apply: a name in use is refused rather than
			// having its spec and value replaced.
			sec, raw, err := c.CreateSecret(cmd.Context(), m)
			if err != nil {
				if cellaclient.CodeOf(err) == "name_taken" {
					return fmt.Errorf("%w\n'latere environments secrets rotate %s' replaces its value, and 'apply -f' changes the rest", err, name)
				}
				return err
			}
			if _, inOrg := labels[labelOrg]; inOrg {
				fprintf(cmd.ErrOrStderr(), "%s belongs to your organization: its owners and admins can attach it to their workloads, and no one, you included, can read its value\n", name)
			}
			return printSecretAnswer(cmd.OutOrStdout(), sec, raw, jsonF)
		},
	}
	cmd.SetFlagErrorFunc(secretFlagError)
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringVar(&s.typ, "type", secretBearer, "how the service takes the credential: "+strings.Join(secretTypes, ", "))
	f.StringSliceVar(&s.hosts, "host", nil, "host the value may be sent to; repeatable, or separated by commas")
	f.IntSliceVar(&s.ports, "port", nil, "port on those hosts; repeatable (default "+strconv.Itoa(v1.DefaultSecretPort)+")")
	f.StringVar(&s.header, "header", "", "the header the value is sent in, for --type header")
	f.StringVar(&s.query, "query", "", "the query parameter the value is sent in, for --type query")
	f.StringVar(&s.username, "username", "", "the username, for --type basic; the password is the value")
	f.StringVar(&s.clientID, "client-id", "", "the OAuth client ID, for --type oauth; the client secret is the value")
	f.StringVar(&s.tokenURL, "token-url", "", "the https:// endpoint the gateway requests an access token at, for --type oauth")
	f.StringVar(&s.tokenScope, "token-scope", "", "the scope of the access token, for --type oauth (optional)")
	f.StringVar(&s.tokenAudience, "token-audience", "", "the audience of the access token, for --type oauth (optional)")
	f.BoolVar(&s.body, "body", false, "also put the value in JSON, form and text request bodies")
	f.BoolVar(&jsonF, "json", false, "print the secret as the API answered it, which never carries the value")
	return cmd
}

// spec is the secret the flags describe, without its value, mapped as the
// console maps the same choice. A flag that belongs to another --type is
// refused rather than ignored, so a header or an endpoint is never dropped.
func (s secretFlags) spec() (v1.SecretSpec, error) {
	if !slices.Contains(secretTypes, s.typ) {
		return v1.SecretSpec{}, fmt.Errorf("--type is one of %s", strings.Join(secretTypes, ", "))
	}
	for _, own := range []struct{ flag, value, typ string }{
		{"header", s.header, secretHeader},
		{"query", s.query, secretQuery},
		{"token-url", s.tokenURL, secretOAuth},
		{"token-scope", s.tokenScope, secretOAuth},
		{"token-audience", s.tokenAudience, secretOAuth},
	} {
		if own.value != "" && s.typ != own.typ {
			return v1.SecretSpec{}, fmt.Errorf("--%s belongs to --type %s", own.flag, own.typ)
		}
	}
	switch {
	case s.typ == secretHeader && strings.TrimSpace(s.header) == "":
		return v1.SecretSpec{}, errors.New("--type header needs --header, the header the value is sent in, such as X-Api-Key")
	case s.typ == secretQuery && strings.TrimSpace(s.query) == "":
		return v1.SecretSpec{}, errors.New("--type query needs --query, the query parameter the value is sent in")
	case s.typ == secretOAuth && strings.TrimSpace(s.tokenURL) == "":
		return v1.SecretSpec{}, errors.New("--type oauth needs --token-url, the endpoint the gateway requests an access token at")
	}
	var hosts []string
	for _, h := range s.hosts {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		return v1.SecretSpec{}, errors.New("--host is required: a secret is sent only to the hosts it names, and there is no setting for every host")
	}
	spec := v1.SecretSpec{
		Kind:   v1.SecretStatic,
		Scope:  v1.SecretScope{Hosts: hosts, Ports: s.ports},
		Inject: v1.SecretInject{Body: s.body},
	}
	switch s.typ {
	case secretBearer, secretOAuth:
		spec.Inject.Header, spec.Inject.Scheme = v1.DefaultInjectHeader, v1.SchemeBearer
	case secretBasic:
		spec.Inject.Header, spec.Inject.Scheme = v1.DefaultInjectHeader, v1.SchemeBasic
	case secretHeader:
		spec.Inject.Header, spec.Inject.Scheme = strings.TrimSpace(s.header), v1.SchemeRaw
	case secretQuery:
		spec.Inject.Query = strings.TrimSpace(s.query)
	}
	if s.typ == secretOAuth {
		spec.Kind = v1.SecretOAuthClientCredentials
		spec.OAuth = &v1.SecretOAuth{
			TokenURL: strings.TrimSpace(s.tokenURL),
			Scope:    strings.TrimSpace(s.tokenScope),
			Audience: strings.TrimSpace(s.tokenAudience),
		}
	}
	return spec, nil
}

func newCeSecretRotateCmd() *cobra.Command {
	var (
		apiURL             string
		username, clientID string
		jsonF              bool
	)
	cmd := &cobra.Command{
		Use:   "rotate <name|id>",
		Short: "Replace a secret's value.",
		Long: `Replace the value of a secret. Every workload that attaches it uses the
new value on its next request, without restarting. Its hosts, its ports
and the way it is sent are kept; 'apply -f' changes those.

The new value is read as 'create' reads one: typed at a prompt without
being shown when standard input is a terminal, and from standard input
otherwise. A basic secret takes --username again and an oauth one
--client-id, because the stored value is the pair and neither half can be
read back.`,
		Example: `  latere environments secrets rotate github-token
  printf %s "$NEW_TOKEN" | latere environments secrets rotate github-token
  latere environments secrets rotate registry --username ci`,
		Args: secretRefArg("the secret's name or id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			current, raw, err := c.GetSecret(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			name, typ := current.Metadata.Name, secretTypeOf(current.Spec)
			half, err := pairHalf(typ, username, clientID)
			if err != nil {
				return err
			}
			value, err := readSecretValue(cmd.InOrStdin(), cmd.ErrOrStderr(), secretValueLabel(typ), name)
			if err != nil {
				return err
			}
			body, err := withSecretValue(raw, joinSecretValue(half, value))
			if err != nil {
				return err
			}
			sec, answer, err := c.ApplySecret(cmd.Context(), name, cellaclient.JSON(body))
			if err != nil {
				return err
			}
			if !jsonF {
				fprintf(cmd.ErrOrStderr(), "rotated %s; every workload that attaches it uses the new value on its next request\n", name)
			}
			return printSecretAnswer(cmd.OutOrStdout(), sec, answer, jsonF)
		},
	}
	cmd.SetFlagErrorFunc(secretFlagError)
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringVar(&username, "username", "", "the username of a basic secret; the password is the value")
	f.StringVar(&clientID, "client-id", "", "the OAuth client ID of an oauth secret; the client secret is the value")
	f.BoolVar(&jsonF, "json", false, "print the secret as the API answered it, which never carries the value")
	return cmd
}

// withSecretValue is the object a read answered, without its status and
// with value in its spec: the body of a rotation. PUT replaces the whole
// object, and the platform admits an update only when it keeps the stored
// labels, so the rotation sends back what the read answered, fields this
// CLI does not model included, as the console's Rotate does.
func withSecretValue(raw []byte, value string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("the secret as read is not a JSON object: %w", err)
	}
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		return nil, errors.New("the secret as read has no spec to keep")
	}
	delete(doc, "status")
	spec["value"] = value
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	// The object's own text is sent as read; HTML escaping would change
	// none of it but its bytes.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode the rotated secret: %w", err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// pairHalf is the half of a two-part value that comes from a flag: the
// username of a basic secret and the client ID of an oauth one, which the
// core stores joined to the other half by a colon. A secret of another type
// takes neither flag.
func pairHalf(typ, username, clientID string) (string, error) {
	half := struct{ secret, flag, value, what string }{}
	switch typ {
	case secretBasic:
		half.secret, half.flag, half.value, half.what = "a basic secret", "username", username, "the password"
		if clientID != "" {
			return "", errors.New("--client-id belongs to an oauth secret")
		}
	case secretOAuth:
		half.secret, half.flag, half.value, half.what = "an oauth secret", "client-id", clientID, "the client secret"
		if username != "" {
			return "", errors.New("--username belongs to a basic secret")
		}
	default:
		if username != "" {
			return "", errors.New("--username belongs to a basic secret")
		}
		if clientID != "" {
			return "", errors.New("--client-id belongs to an oauth secret")
		}
		return "", nil
	}
	switch {
	case half.value == "":
		return "", fmt.Errorf("%s needs --%s; %s is the value, read from standard input or the prompt", half.secret, half.flag, half.what)
	case strings.Contains(half.value, ":"):
		return "", fmt.Errorf("--%s cannot contain a colon: the secret stores it and %s joined by one", half.flag, half.what)
	}
	return half.value, nil
}

// joinSecretValue is the value the core stores: the flag's half and the
// value joined by a colon, or the value alone when there is no half.
func joinSecretValue(half, value string) string {
	if half == "" {
		return value
	}
	return half + ":" + value
}

// secretValueLabel is what the prompt asks for: the value itself, or the
// half of a two-part value that is the secret one.
func secretValueLabel(typ string) string {
	switch typ {
	case secretBearer:
		return "Token"
	case secretBasic:
		return "Password"
	case secretOAuth:
		return "Client secret"
	}
	return "Value"
}

// readSecretValue reads a secret's value: typed at a prompt on prompts
// without echo when in is a terminal, and otherwise in whole, without one
// trailing line ending, so `echo` and `printf %s` give the same value.
// Nothing else is trimmed. The read is bounded by the most a secret holds.
func readSecretValue(in io.Reader, prompts io.Writer, label, name string) (string, error) {
	what := strings.ToLower(label)
	if typed := typedSecret(in); typed != nil {
		fprintf(prompts, "%s for %s: ", label, name)
		value, err := typed()
		// The line the person ended was not echoed either.
		fprintln(prompts)
		if err != nil {
			return "", fmt.Errorf("read the %s: %w", what, err)
		}
		if value == "" {
			return "", fmt.Errorf("no %s was typed. Nothing was changed", what)
		}
		return value, nil
	}
	body, err := io.ReadAll(io.LimitReader(in, int64(manifest.MaxSecretValueBytes+len("\r\n")+1)))
	if err != nil {
		return "", fmt.Errorf("read the %s from standard input: %w", what, err)
	}
	value := string(body)
	if rest, ok := strings.CutSuffix(value, "\n"); ok {
		value = strings.TrimSuffix(rest, "\r")
	}
	switch {
	case value == "":
		return "", fmt.Errorf("standard input held no %s: pipe it in, or run the command at a terminal to type it. Nothing was changed", what)
	case len(value) > manifest.MaxSecretValueBytes:
		return "", fmt.Errorf("the %s is longer than %d bytes, the most a secret holds. Nothing was changed", what, manifest.MaxSecretValueBytes)
	}
	return value, nil
}

// secretRefArg takes exactly one argument, what. Its refusal names no
// argument, since a value pasted on the command line may be among them.
func secretRefArg(what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("%s takes %s alone. A value is never an argument, which a shell history would keep: pipe it to standard input, or type it at the prompt", cmd.CommandPath(), what)
		}
		return nil
	}
}

// secretFlagError refuses a flag that cannot be read without repeating it:
// pflag's own refusal quotes what it was given, and that may be a value.
func secretFlagError(cmd *cobra.Command, _ error) error {
	return fmt.Errorf("a flag given to %s could not be read, and is not repeated here in case it holds a value. A value is never a flag's: '%s --help' lists the flags", cmd.CommandPath(), cmd.CommandPath())
}

// ---- apply / list / get / delete ----

// newCeSecretApplyCmd registers `secrets apply -f <file>`, the workload
// apply's counterpart: the manifest is sent as written with the account's
// labels added, under the name it declares when it declares one.
func newCeSecretApplyCmd() *cobra.Command {
	var (
		file   string
		apiURL string
		jsonF  bool
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Create or update a secret from its manifest.",
		Long: `Create a secret from a declarative manifest, or update the one it names.
An update replaces the secret's hosts, ports and the way it is sent with
the manifest's, and its value when the manifest carries one; without one,
the stored value is kept.

A manifest in YAML or JSON:

  apiVersion: cella.latere.ai/v1beta1
  kind: Secret
  metadata:
    name: github-token
  spec:
    scope:
      hosts: [api.github.com]
      ports: [` + strconv.Itoa(v1.DefaultSecretPort) + `]                  # Optional. ` + strconv.Itoa(v1.DefaultSecretPort) + ` when omitted.
    inject:
      header: Authorization         # Or query: <parameter>, never both.
      scheme: bearer                # bearer, basic or raw.
    value: <the credential>         # Required to create; never returned.

A file with a value in it holds the credential. To keep it out of the file,
create the secret with 'create', which reads the value from standard input,
and apply manifests without a value after that.

The CLI adds your account's labels (platform.latere.ai/tenant, /principal
and, in an organization, /org) for your current context before it sends
the manifest, as 'latere environments apply' does; the platform refuses a
create without them. The manifest's own labels and comments are kept.

How each way a secret is sent maps onto these fields:
https://platform.latere.ai/docs/environments/console#secrets`,
		Example: `  latere environments secrets apply -f secret.yaml
  cat secret.json | latere environments secrets apply -f -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(file) == "" {
				return fmt.Errorf("-f is required (path to a secret manifest, or - for stdin)")
			}
			body, err := readManifestBody(file, cmd.InOrStdin())
			if err != nil {
				return err
			}
			if manifestKind(body) == v1.KindSandbox {
				return errors.New("this manifest is a workload (kind Sandbox): apply it with 'latere environments apply -f'")
			}
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			if body, err = labeledManifest(cmd.Context(), body); err != nil {
				return err
			}
			m := manifestOf(body)
			var (
				sec v1.Secret
				raw []byte
			)
			if name := manifestName(body); name != "" {
				sec, raw, err = c.ApplySecret(cmd.Context(), name, m)
			} else {
				sec, raw, err = c.CreateSecret(cmd.Context(), m)
			}
			if err != nil {
				return err
			}
			return printSecretAnswer(cmd.OutOrStdout(), sec, raw, jsonF)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "file", "f", "", "path to a secret manifest in YAML or JSON, or - for stdin")
	_ = cmd.MarkFlagRequired("file")
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.BoolVar(&jsonF, "json", false, "print the secret as the API answered it, which never carries the value")
	return cmd
}

func newCeSecretListCmd() *cobra.Command {
	var (
		apiURL string
		jsonF  bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your secrets.",
		Long: `List the secrets the current login may read, each without its value: how
it is sent, its hosts and ports, how many workloads attach it, and when it
was created and last used.`,
		Example: `  latere environments secrets list
  latere environments secrets list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			secs, raws, err := c.ListSecrets(cmd.Context(), cellaclient.ListOptions{})
			if err != nil {
				return err
			}
			if jsonF {
				if raws == nil {
					raws = []json.RawMessage{}
				}
				return printJSON(cmd.OutOrStdout(), raws)
			}
			return printSecretList(cmd.OutOrStdout(), secs)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	cmd.Flags().BoolVar(&jsonF, "json", false, "JSON output")
	return cmd
}

func newCeSecretGetCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "get <name|id>",
		Short: "Get a secret by name or id.",
		Long:  "Fetch one secret by name or id and print it as the API answered, in JSON. The answer never carries the value.",
		Example: `  latere environments secrets get github-token
  latere environments secrets get sec_01k5x6j9c2`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			_, raw, err := c.GetSecret(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printRawJSON(cmd.OutOrStdout(), raw)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

func newCeSecretDeleteCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a secret and its value.",
		Long: `Delete a secret and its value. A workload that attaches it keeps its
variable, but the gateway no longer replaces the placeholder in it, so the
workload's requests go out without the credential. The command says how
many workloads still attached the secret.`,
		Example: `  latere environments secrets delete github-token`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			raw, err := c.Delete(cmd.Context(), cellaclient.KindSecret, args[0])
			if err != nil {
				return err
			}
			stderr := cmd.ErrOrStderr()
			fprintf(stderr, "deleted secret %s\n", args[0])
			// The secret is gone whatever the answer says; an answer that
			// does not read only leaves the count of workloads unsaid.
			var gone v1.Secret
			if err := json.Unmarshal(raw, &gone); err != nil {
				fprintf(stderr, "the answer does not say which workloads attached it: %v\n", err)
				return nil
			}
			if n := gone.Status.MountedBy; n > 0 {
				fprintf(stderr, "%s attached it: the variable stays in their environment, and their requests go out without the credential\n", countWorkloads(n))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

// manifestKind is the kind the manifest declares, or empty when it declares
// none or cannot be read here.
func manifestKind(body []byte) string {
	var m struct {
		Kind string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(body, &m); err != nil {
		return ""
	}
	return strings.TrimSpace(m.Kind)
}

// ---- output ----

// printSecretAnswer writes a secret a write answered: the API's own object
// with --json, the record otherwise. Neither carries the value.
func printSecretAnswer(out io.Writer, s v1.Secret, raw []byte, jsonF bool) error {
	if jsonF {
		return printRawJSON(out, raw)
	}
	return printSecret(out, s)
}

func printSecretList(out io.Writer, secs []v1.Secret) error {
	if len(secs) == 0 {
		if _, err := fmt.Fprintln(out, "No secrets are visible to this login."); err != nil {
			return fmt.Errorf("write secret list: %w", err)
		}
		return nil
	}
	for i, s := range secs {
		if i > 0 {
			if _, err := fmt.Fprintln(out); err != nil {
				return fmt.Errorf("write secret list separator: %w", err)
			}
		}
		if err := printSecret(out, s); err != nil {
			return err
		}
	}
	return nil
}

// printSecret writes one secret as a record: what it is, where its value is
// sent, how many workloads attach it, and when it was written and used. The
// record has no field for a value.
func printSecret(out io.Writer, s v1.Secret) error {
	var record strings.Builder
	field := func(label, value string) {
		record.WriteString(formatWrappedField(label, value))
	}
	field("secret", defaultStr(s.Metadata.Name, "-"))
	field("id", s.Status.ID)
	field("sent", secretSentence(s.Spec))
	field("hosts", strings.Join(s.Spec.Scope.Hosts, ", "))
	var ports []string
	for _, p := range s.Spec.Scope.Ports {
		ports = append(ports, strconv.Itoa(p))
	}
	field("ports", strings.Join(ports, ", "))
	if s.Status.Version > 0 {
		field("version", strconv.Itoa(s.Status.Version))
	}
	field("attached", "to "+countWorkloads(s.Status.MountedBy))
	if !s.Status.CreatedAt.IsZero() {
		field("created", humanAge(s.Status.CreatedAt)+" ago")
	}
	if !s.Status.UpdatedAt.IsZero() && !s.Status.UpdatedAt.Equal(s.Status.CreatedAt) {
		field("updated", humanAge(s.Status.UpdatedAt)+" ago")
	}
	if !s.Status.LastUsedAt.IsZero() {
		field("last used", humanAge(s.Status.LastUsedAt)+" ago")
	}
	if _, err := fmt.Fprint(out, record.String()); err != nil {
		return fmt.Errorf("write secret details for %q: %w", s.Metadata.Name, err)
	}
	return nil
}

// countWorkloads is n workloads in words.
func countWorkloads(n int) string {
	switch n {
	case 0:
		return "no workload"
	case 1:
		return "1 workload"
	}
	return strconv.Itoa(n) + " workloads"
}

// secretTypeOf is the --type a stored spec reads back as, as the console
// reads one.
func secretTypeOf(s v1.SecretSpec) string {
	header := defaultStr(s.Inject.Header, v1.DefaultInjectHeader)
	switch {
	case s.Kind == v1.SecretOAuthClientCredentials:
		return secretOAuth
	case s.Inject.Query != "":
		return secretQuery
	case s.Inject.Scheme == v1.SchemeBasic:
		return secretBasic
	case strings.EqualFold(header, v1.DefaultInjectHeader) && defaultStr(s.Inject.Scheme, v1.SchemeBearer) == v1.SchemeBearer:
		return secretBearer
	}
	return secretHeader
}

// secretSentence says where in a request the gateway puts a secret's value.
func secretSentence(s v1.SecretSpec) string {
	header := defaultStr(s.Inject.Header, v1.DefaultInjectHeader)
	var where string
	switch secretTypeOf(s) {
	case secretOAuth:
		endpoint := "the token endpoint"
		if s.OAuth != nil && s.OAuth.TokenURL != "" {
			endpoint = s.OAuth.TokenURL
		}
		where = "as an access token from " + endpoint + ", in the " + header + " header"
	case secretBearer:
		where = "as a bearer token in the " + header + " header"
	case secretBasic:
		where = "as basic credentials in the " + header + " header"
	case secretQuery:
		where = "in the " + s.Inject.Query + " query parameter"
	default:
		where = "in the " + header + " header"
	}
	if s.Inject.Body {
		where += ", and in the request body"
	}
	return where
}
