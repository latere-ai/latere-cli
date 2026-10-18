// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"

	"gopkg.in/yaml.v3"
)

// The platform's decider admits a create at Environments only when the
// object carries the caller's tenant, principal and, in an organization,
// org labels under platform.latere.ai/, and the tenant label is a hash of
// the context. The platform answers them for the token's context at
// tenantLabelsPath, and every create the CLI makes carries them, as every
// create the console makes does.

// tenantLabelsPath is the platform's read of the labels a create carries.
const tenantLabelsPath = "/v1/account/environments/context"

// tenantLabels reads the labels of the login's current context from the
// platform at platformURL ("" for the default), with a bearer for the
// platform's audience. A read the platform refuses ends the command: without
// the labels a create is refused.
func tenantLabels(ctx context.Context, platformURL string) (map[string]string, error) {
	var answer struct {
		Tenant struct {
			Labels map[string]string `json:"labels"`
		} `json:"tenant"`
	}
	if err := platformCall(ctx, platformURL, "", http.MethodGet, tenantLabelsPath, nil, &answer); err != nil {
		return nil, fmt.Errorf("cannot read your account's labels for the workload from the platform: %w", wrapReposErr(err))
	}
	if len(answer.Tenant.Labels) == 0 {
		return nil, errors.New("the platform answered no labels for your account, and Environments refuses a create without them")
	}
	return answer.Tenant.Labels, nil
}

// stampTenantLabels answers the manifest with labels set under
// metadata.labels, each one replacing a value the manifest gave the same
// key, the manifest's other labels and fields kept, and the syntax it was
// written in kept: a body that begins with a brace is JSON to the control
// plane and stays JSON; YAML keeps its comments. A manifest that carries
// every label already is answered as written, and so is one this cannot
// read as a single mapping whose metadata and labels are mappings, so the
// refusal the person reads is the control plane's, with its field.
func stampTenantLabels(body []byte, labels map[string]string) ([]byte, error) {
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		return stampJSON(body, labels)
	}
	return stampYAML(body, labels)
}

func stampJSON(body []byte, labels map[string]string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return body, nil //nolint:nilerr // a manifest this cannot read is sent as written, so the control plane names the field
	}
	var ok bool
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return body, nil
	}
	// An absent or null member is an empty one; any other value is the
	// control plane's to refuse.
	metadata := map[string]any{}
	if raw := doc["metadata"]; raw != nil {
		if metadata, ok = raw.(map[string]any); !ok {
			return body, nil
		}
	}
	current := map[string]any{}
	if raw := metadata["labels"]; raw != nil {
		if current, ok = raw.(map[string]any); !ok {
			return body, nil
		}
	}
	carries := true
	for k, v := range labels {
		if current[k] != v {
			carries = false
		}
	}
	if carries {
		return body, nil
	}
	merged := maps.Clone(current)
	for k, v := range labels {
		merged[k] = v
	}
	metadata["labels"] = merged
	doc["metadata"] = metadata
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	// The manifest's own text is a value the control plane reads as
	// written; HTML escaping would change none of it but its bytes.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode the manifest with your account's labels: %w", err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func stampYAML(body []byte, labels map[string]string) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(body))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return body, nil //nolint:nilerr // a manifest this cannot read is sent as written, so the control plane names the field
	}
	// A second document is the control plane's to refuse; re-encoding the
	// first alone would drop it.
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return body, nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return body, nil
	}
	metadata, ok := mappingAt(doc.Content[0], "metadata")
	if !ok {
		return body, nil
	}
	current, ok := mappingAt(metadata, "labels")
	if !ok {
		return body, nil
	}
	carries := true
	for k, v := range labels {
		if value := valueOf(current, k); value == nil || value.Kind != yaml.ScalarNode || value.Value != v || value.ShortTag() != "!!str" {
			carries = false
		}
	}
	if carries {
		return body, nil
	}
	// In key order, so a stamped manifest is the same bytes on every run.
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		// Quoted, so a value such as a numeric principal stays a string.
		value := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: labels[k], Style: yaml.DoubleQuotedStyle}
		if existing := valueOf(current, k); existing != nil {
			*existing = *value
			continue
		}
		current.Content = append(current.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, value)
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("encode the manifest with your account's labels: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode the manifest with your account's labels: %w", err)
	}
	return out.Bytes(), nil
}

// mappingAt is the mapping under key in m, added empty when m has no such
// key or holds null there, and false when the key holds anything else.
func mappingAt(m *yaml.Node, key string) (*yaml.Node, bool) {
	if value := valueOf(m, key); value != nil {
		if value.Kind == yaml.ScalarNode && value.ShortTag() == "!!null" {
			*value = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		return value, value.Kind == yaml.MappingNode
	}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	return value, true
}

// valueOf is the value node of key in the mapping m, or nil.
func valueOf(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// labeledManifest reads the login's labels and answers body carrying them.
func labeledManifest(ctx context.Context, body []byte) ([]byte, error) {
	labels, err := tenantLabels(ctx, "")
	if err != nil {
		return nil, err
	}
	return stampTenantLabels(body, labels)
}
