// SPDX-License-Identifier: AGPL-3.0-only

package plugins

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/plugins/fence"
)

func TestDigestIgnoresOrderAndWhitespace(t *testing.T) {
	left := kindPlugin("alpha", "widget", []string{fence.PermViewsProvide, fence.PermNodesContribute}, "{\n  \"type\": \"object\", \"$id\": \"urn:alpha\"\n}")
	right := kindPlugin("alpha", "widget", []string{fence.PermNodesContribute, fence.PermViewsProvide}, `{"$id":"urn:alpha","type":"object"}`)
	a, err := Digest(left)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Digest(right)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || len(a) != 64 {
		t.Fatalf("digest %s vs %s", a, b)
	}
	right.Manifest.Owner = "other"
	changed, err := Digest(right)
	if err != nil || changed == a {
		t.Fatalf("owner change digest = %s err=%v", changed, err)
	}
}

func TestRegisterRejectsClosedFailures(t *testing.T) {
	reg := NewRegistry()
	plug := mustPlugin(t, kindPlugin("alpha", "widget", []string{fence.PermNodesContribute}, `{"type":"object","$id":"urn:alpha"}`))
	if err := reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(plug); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate = %v", err)
	}

	mismatch := plug
	mismatch.Manifest.ID = "beta"
	mismatch.Manifest.NodeKinds[0].Slug = "other"
	mismatch.Manifest.NodeKinds[0].FieldSchema = json.RawMessage(`{"$id":"urn:beta","type":"object"}`)
	sum, err := Digest(mismatch)
	if err != nil {
		t.Fatal(err)
	}
	mismatch.Manifest.DigestSHA256 = strings.Repeat("ab", 32)
	if sum == mismatch.Manifest.DigestSHA256 {
		t.Fatal("fixture digest collided")
	}
	if err := reg.Register(mismatch); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("mismatch = %v", err)
	}
	mismatch.Manifest.DigestSHA256 = sum
	mismatch.Manifest.Permissions = []string{fence.PermNodesContribute, "shell.exec"}
	if _, err := Digest(mismatch); err == nil || !strings.Contains(err.Error(), "unknown permission") {
		t.Fatalf("unknown = %v", err)
	}

	collision := mustPlugin(t, kindPlugin("gamma", "widget", []string{fence.PermNodesContribute}, `{"type":"object","$id":"urn:gamma"}`))
	if err := reg.Register(collision); err == nil || !strings.Contains(err.Error(), "node kind") {
		t.Fatalf("kind collision = %v", err)
	}
	schema := mustPlugin(t, kindPlugin("delta", "gadget", []string{fence.PermNodesContribute}, `{"type":"object","$id":"urn:alpha"}`))
	if err := reg.Register(schema); err == nil || !strings.Contains(err.Error(), "field schema") {
		t.Fatalf("schema collision = %v", err)
	}

	outside := kindPlugin("epsilon", "lone", []string{fence.PermNodesContribute}, `{"type":"object"}`)
	outside.Manifest.AgentTools = []Capability{{ID: "run", Permission: fence.PermStageDeploy}}
	outside.Tools = toolStub{}
	if _, err := Digest(outside); err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("ceiling = %v", err)
	}
	bare := kindPlugin("zeta", "bare", []string{fence.PermNodesContribute}, `{"type":"object"}`)
	bare.Kinds = nil
	if _, err := Digest(bare); err == nil || !strings.Contains(err.Error(), "implementation") {
		t.Fatalf("implementation = %v", err)
	}
}

func TestBuiltinIsSealed(t *testing.T) {
	reg, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	items := reg.list()
	if len(items) != 0 {
		t.Fatalf("builtin = %#v", ids(items))
	}
	if err := reg.Register(mustPlugin(t, kindPlugin("widget", "widget", nil, `{}`))); err == nil {
		t.Fatal("sealed registry accepted a plugin")
	}
}

func TestCallCarriesOnlyPrincipalAndGrant(t *testing.T) {
	typ := reflect.TypeOf(Call{})
	if typ.NumField() != 2 || typ.Field(0).Name != "Principal" || typ.Field(1).Name != "Grant" {
		t.Fatalf("Call fields = %d", typ.NumField())
	}
	var grant Grant
	if grant.Allows(fence.PermStageDeploy) || len(grant.Names()) != 0 {
		t.Fatal("zero grant allows a permission")
	}
}

type kindContributor struct{}

func (kindContributor) NodeKinds(context.Context, Call) ([]NodeKind, error) { return nil, nil }

type toolStub struct{}

func (toolStub) Invoke(context.Context, Call, string, any) (any, error) { return nil, nil }

func kindPlugin(id, slug string, perms []string, schema string) Plugin {
	raw := json.RawMessage(schema)
	return Plugin{
		Manifest: Manifest{
			ID: id, Version: "1", Owner: "test", Permissions: perms,
			NodeKinds: []NodeKind{{Slug: slug, FieldSchema: raw}},
		},
		Kinds: kindContributor{},
	}
}

func mustPlugin(t *testing.T, p Plugin) Plugin {
	t.Helper()
	sum, err := Digest(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Manifest.DigestSHA256 = sum
	return p
}

func ids(items []Plugin) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Manifest.ID
	}
	return out
}
