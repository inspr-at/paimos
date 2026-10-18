// SPDX-License-Identifier: AGPL-3.0-only

package plugins

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestEmptyPluginArraysStayArrays(t *testing.T) {
	manifest := Manifest{
		Permissions:    cloneStrings([]string{}),
		NodeKinds:      []NodeKind{{AllowedChildKinds: cloneStrings([]string{})}},
		Views:          []View{{Panels: cloneStrings([]string{})}},
		WorkflowSteps:  []WorkflowStep{{Gates: cloneStrings([]string{})}},
		AgentTools:     cloneCaps([]Capability{}),
		Integrations:   cloneCaps([]Capability{}),
		BackgroundJobs: cloneCaps(nil),
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{
		`"permissions":[]`,
		`"allowed_child_kinds":[]`,
		`"panels":[]`,
		`"gates":[]`,
		`"agent_tools":[]`,
		`"integrations":[]`,
		`"background_jobs":[]`,
	} {
		if !bytes.Contains(raw, []byte(token)) {
			t.Fatalf("missing %s in %s", token, raw)
		}
	}
	for _, token := range []string{
		`"permissions":null`,
		`"allowed_child_kinds":null`,
		`"panels":null`,
		`"gates":null`,
		`"agent_tools":null`,
		`"integrations":null`,
		`"background_jobs":null`,
	} {
		if bytes.Contains(raw, []byte(token)) {
			t.Fatalf("empty array encoded as null (%s) in %s", token, raw)
		}
	}
	if cloneStrings(nil) == nil {
		t.Fatal("nil permissions became null")
	}
	kept := []string{"nodes.contribute"}
	cloned := cloneStrings(kept)
	kept[0] = "changed"
	if len(cloned) != 1 || cloned[0] != "nodes.contribute" {
		t.Fatalf("cloneStrings alias: %#v", cloned)
	}
}

func TestEmptyInstallationPermissionsStayArray(t *testing.T) {
	granted, err := normalizeGrant([]string{}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(Installation{Permissions: cloneStrings(granted)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"permissions":[]`)) || bytes.Contains(raw, []byte(`"permissions":null`)) {
		t.Fatalf("installation permissions: %s", raw)
	}
}
