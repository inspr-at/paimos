// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// The document server is /api, so path keys are relative to it. A key that
// already starts with /api publishes /api/api/…. These three operations also
// have to carry the shared session-or-agent-key requirement; the document has
// no root security requirement, so an omitted security block publishes them
// as unauthenticated.
func TestEngineAdmissionPathsAreServerRelativeAndAuthenticated(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		t.Fatal("openapi root is not a single document")
	}
	root := doc.Content[0]
	server := mappingScalar(t, sequenceItem(t, mappingValue(t, root, "servers"), 0), "url")
	if server != "/api" {
		t.Fatalf("document server = %q, want /api", server)
	}
	paths := mappingValue(t, root, "paths")
	auth := anchorValue(root, "auth")
	if auth == nil {
		t.Fatal("shared security anchor &auth is missing")
	}
	if got := flowSecuritySchemes(auth); got != "session,agentKey" {
		t.Fatalf("&auth schemes = %q, want session,agentKey", got)
	}
	cases := []struct{ path, method string }{
		{"/engine/admission", "post"},
		{"/projects/{projectId}/admission-settings", "get"},
		{"/projects/{projectId}/admission-settings", "put"},
	}
	for _, tc := range cases {
		item := mappingValue(t, paths, tc.path)
		if item == nil {
			t.Fatalf("missing %s %s; a /api-prefixed key joins with server %s to %s/api…", tc.method, tc.path, server, server)
		}
		if override := mappingValue(t, item, "servers"); override != nil && override.Kind != 0 {
			t.Fatalf("%s overrides servers; the live handler stays under %s", tc.path, server)
		}
		op := mappingValue(t, item, tc.method)
		if op == nil {
			t.Fatalf("missing %s %s", tc.method, tc.path)
		}
		security := mappingValue(t, op, "security")
		if security == nil || security.Kind != yaml.AliasNode || security.Value != "auth" {
			t.Fatalf("%s %s security = %#v, want alias *auth", tc.method, tc.path, security)
		}
		joined := server + tc.path
		if joined != "/api"+tc.path {
			t.Fatalf("join(%s, %s) = %s", server, tc.path, joined)
		}
	}
	for _, doubled := range []string{"/api/engine/admission", "/api/projects/{projectId}/admission-settings"} {
		if mappingValue(t, paths, doubled) != nil {
			t.Fatalf("path %s joins with server %s to %s%s", doubled, server, server, doubled)
		}
	}
}

func mappingValue(t *testing.T, node *yaml.Node, key string) *yaml.Node {
	t.Helper()
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func sequenceItem(t *testing.T, node *yaml.Node, index int) *yaml.Node {
	t.Helper()
	if node == nil || node.Kind != yaml.SequenceNode || index < 0 || index >= len(node.Content) {
		t.Fatalf("sequence item %d missing", index)
	}
	return node.Content[index]
}

func mappingScalar(t *testing.T, node *yaml.Node, key string) string {
	t.Helper()
	value := mappingValue(t, node, key)
	if value == nil || value.Kind != yaml.ScalarNode {
		t.Fatalf("scalar %s missing", key)
	}
	return value.Value
}

func anchorValue(node *yaml.Node, name string) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Anchor == name {
		return node
	}
	for _, child := range node.Content {
		if found := anchorValue(child, name); found != nil {
			return found
		}
	}
	return nil
}

// flowSecuritySchemes reads a whole-line flow sequence [{session: []}, {agentKey: []}].
func flowSecuritySchemes(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.SequenceNode {
		return ""
	}
	names := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.MappingNode || len(item.Content) < 2 {
			return ""
		}
		names = append(names, item.Content[0].Value)
	}
	if len(names) != 2 {
		return ""
	}
	return names[0] + "," + names[1]
}
