// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/decisiondesk"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workqueue"
)

// Risk: a default edit silently changes copy enforcement, a keep is forgotten,
// stale decisions overwrite newer state, or the Desk discloses admin decisions.
func TestDefinitionDefaultChangesWaitForAuthorizedPerCheckDecisions(t *testing.T) {
	p := newPrincipal(t, "definition-changes")
	base := map[string][]workqueue.EntryRule{"in_progress": {{ID: "estimate_set"}, {ID: "acceptance_present"}, {ID: "manual:review", Label: "Ready checked"}}}
	send := func(who tenant.Principal, method, path string, body any, want int) []byte {
		t.Helper()
		text := ""
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			text = string(raw)
		}
		code, raw := call(t, &who, method, path, text)
		if code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, code, want, raw)
		}
		return raw
	}
	var workspace workqueue.EntryDefinition
	if err := json.Unmarshal(send(p, "PUT", "/api/settings/definitions", map[string]any{"expected_revision": nil, "checks": base}, 200), &workspace); err != nil {
		t.Fatal(err)
	}
	type scenario struct {
		kind, id, choice string
		project          nodeJSON
		copy             workqueue.EntryDefinition
	}
	cases := []scenario{}
	for _, kind := range []string{"new", "changed", "removed"} {
		for _, choice := range []string{"accept", "keep"} {
			project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":%q}`, kindBySlug(t, p, "project").ID, kind+" "+choice))
			var copy workqueue.EntryDefinition
			if err := json.Unmarshal(send(p, "PUT", "/api/projects/"+project.ID+"/definitions", map[string]any{"expected_revision": nil, "expected_workspace_revision": workspace.Revision, "copied_from_revision": workspace.Revision, "checks": base}, 200), &copy); err != nil {
				t.Fatal(err)
			}
			id := map[string]string{"new": "no_active_build", "changed": "estimate_set", "removed": "acceptance_present"}[kind]
			cases = append(cases, scenario{kind, id, choice, project, copy})
		}
	}
	inherited := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Inherits default"}`, kindBySlug(t, p, "project").ID))
	updated := map[string][]workqueue.EntryRule{"in_progress": {{ID: "estimate_set", Condition: "ui"}, {ID: "no_active_build"}, {ID: "manual:review", Label: "Ready checked"}}}
	if err := json.Unmarshal(send(p, "PUT", "/api/settings/definitions", map[string]any{"expected_revision": workspace.Revision, "checks": updated}, 200), &workspace); err != nil {
		t.Fatal(err)
	}
	var inheritedDefinition workqueue.EntryDefinition
	if err := json.Unmarshal(send(p, "GET", "/api/projects/"+inherited.ID+"/definitions", nil, 200), &inheritedDefinition); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inheritedDefinition.Checks, updated) || inheritedDefinition.Source != "workspace" {
		t.Fatalf("inherited policy did not update immediately: %+v", inheritedDefinition)
	}
	var summary workspaceDefinitionContext
	if err := json.Unmarshal(send(p, "GET", "/api/settings/definitions/context?limit=2", nil, 200), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.DifferCount != 6 || len(summary.Projects) != 2 || summary.Next == nil || len(summary.UsesDefault) != 1 {
		t.Fatalf("incorrect exact counts or keyset: %+v", summary)
	}
	next := send(p, "GET", "/api/settings/definitions/context?limit=2&after="+*summary.Next, nil, 200)
	var nextSummary workspaceDefinitionContext
	if err := json.Unmarshal(next, &nextSummary); err != nil {
		t.Fatal(err)
	}
	if len(nextSummary.Projects) != 2 || nextSummary.Projects[0].ProjectID <= *summary.Next {
		t.Fatalf("invalid keyset page: %s", next)
	}
	for _, scenario := range cases {
		t.Run(scenario.kind+"/"+scenario.choice, func(t *testing.T) {
			path := "/api/projects/" + scenario.project.ID + "/definitions"
			var current entryDefinitionContext
			if err := json.Unmarshal(send(p, "GET", path+"/context", nil, 200), &current); err != nil {
				t.Fatal(err)
			}
			if len(current.Pending) != 3 || !reflect.DeepEqual(current.Definition.Checks, base) || !reflect.DeepEqual(current.Base, base) || current.Definition.CreatedAt == nil || !current.Definition.CreatedAt.Equal(*scenario.copy.CreatedAt) {
				t.Fatalf("default silently changed copy or provenance: %+v", current)
			}
			if len(current.Admins) != 1 || current.Admins[0].ID != p.ID {
				t.Fatalf("admin names missing: %+v", current.Admins)
			}
			// Actual status enforcement still requires an estimate on this unlabelled ticket.
			n := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"No estimate","fields":{"acceptance_criteria":"Test the default copy"}}`, kindBySlug(t, p, "work").ID, scenario.project.ID))
			var ticked workqueue.EntryChecks
			if err := json.Unmarshal(send(p, "PUT", "/api/nodes/"+n.ID+"/entry-checks/manual:review", map[string]any{"expected_revision": n.UpdatedAt, "checked": true}, 200), &ticked); err != nil {
				t.Fatal(err)
			}
			agent := estimateAgent(t, p)
			code, raw := call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"state":"in_progress"}`)
			if code != 409 || !strings.Contains(string(raw), `"error":"entry_checks_failed"`) || !strings.Contains(string(raw), `"id":"estimate_set"`) {
				t.Fatalf("pending copy enforcement changed: %d %s", code, raw)
			}
			body := map[string]any{"expected_revision": current.Definition.Revision, "expected_workspace_revision": workspace.Revision, "check_id": scenario.id, "decision": scenario.choice}
			member := tenant.Principal{ID: addPrincipalIn(t, p.TenantID, "Definition viewer").ID, TenantID: p.TenantID, Kind: tenant.Person}
			if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, p.TenantID, member.ID, scenario.project.ID); err != nil {
				t.Fatal(err)
			}
			send(member, "POST", path+"/decisions", body, 403)
			send(agent, "POST", path+"/decisions", body, 403)
			page, err := decisiondesk.New(appPool).Read(t.Context(), member, 100, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page.Items {
				if item.Kind == "definition_change" {
					t.Fatalf("member saw admin request: %+v", item)
				}
			}
			stale := map[string]any{"expected_revision": current.Definition.Revision, "expected_workspace_revision": workspace.Revision.Add(-time.Second), "check_id": scenario.id, "decision": scenario.choice}
			send(p, "POST", path+"/decisions", stale, 409)
			if err := json.Unmarshal(send(p, "POST", path+"/decisions", body, 200), &current); err != nil {
				t.Fatal(err)
			}
			if len(current.Pending) != 2 || !reflect.DeepEqual(current.Base, base) || !current.Definition.CreatedAt.Equal(*scenario.copy.CreatedAt) {
				t.Fatalf("decision corrupted copy metadata: %+v", current)
			}
			var selected *workqueue.EntryRule
			for _, rule := range current.Definition.Checks["in_progress"] {
				if rule.ID == scenario.id {
					copy := rule
					selected = &copy
				}
			}
			if scenario.choice == "keep" && !reflect.DeepEqual(current.Definition.Checks, base) {
				t.Fatalf("keep changed enforcement: %+v", current.Definition.Checks)
			}
			if scenario.choice == "keep" {
				if !current.Definition.Revision.Equal(scenario.copy.Revision) {
					t.Fatal("acknowledgement changed the enforced policy revision")
				}
				var preview workqueue.EntryChecks
				if err := json.Unmarshal(send(p, "GET", "/api/nodes/"+n.ID+"/entry-checks", nil, 200), &preview); err != nil {
					t.Fatal(err)
				}
				passed := false
				for _, block := range preview.Blocks {
					for _, rule := range block.Checks {
						if rule.ID == "manual:review" {
							passed = rule.Passed
						}
					}
				}
				if !passed {
					t.Fatal("keeping the copy erased an existing person tick")
				}
			}
			if scenario.choice == "accept" {
				switch scenario.kind {
				case "new":
					if selected == nil {
						t.Fatal("new rule not added")
					}
				case "removed":
					if selected != nil {
						t.Fatal("removed rule still enforced")
					}
				case "changed":
					if selected == nil || selected.Condition != "ui" {
						t.Fatal("condition not applied")
					}
					send(p, "PUT", "/api/nodes/"+n.ID+"/entry-checks/manual:review", map[string]any{"expected_revision": ticked.Revision, "checked": true}, 200)
					code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"state":"in_progress"}`)
					if code != 200 {
						t.Fatalf("accepted condition not enforced: %d %s", code, raw)
					}
				}
			}
			send(p, "POST", path+"/decisions", body, 409)
			for len(current.Pending) > 0 {
				change := current.Pending[0]
				body = map[string]any{"expected_revision": current.Definition.Revision, "expected_workspace_revision": workspace.Revision, "check_id": change.CheckID, "decision": "keep"}
				if err := json.Unmarshal(send(p, "POST", path+"/decisions", body, 200), &current); err != nil {
					t.Fatal(err)
				}
			}
			// An ordinary edit preserves the acknowledgements and original copy date.
			var saved workqueue.EntryDefinition
			if err := json.Unmarshal(send(p, "PUT", path, map[string]any{"expected_revision": current.Definition.Revision, "checks": current.Definition.Checks}, 200), &saved); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(send(p, "GET", path+"/context", nil, 200), &current); err != nil {
				t.Fatal(err)
			}
			if len(current.Pending) != 0 || !current.Definition.CreatedAt.Equal(*scenario.copy.CreatedAt) {
				t.Fatal("ordinary edit forgot decisions or copy date")
			}
			page, err = decisiondesk.New(appPool).Read(t.Context(), p, 100, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page.Items {
				if item.Kind == "definition_change" && item.ProjectID == scenario.project.ID {
					t.Fatal("settled request remains on Desk")
				}
			}
		})
	}
	// A changed version of a kept check waits again; acknowledgements are per version.
	updated["in_progress"][0].Condition = "code"
	send(p, "PUT", "/api/settings/definitions", map[string]any{"expected_revision": workspace.Revision, "checks": updated}, 200)
	page, err := decisiondesk.New(appPool).Read(t.Context(), p, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range page.Items {
		if item.Kind == "definition_change" {
			count++
			if item.DefinitionChange == nil || item.DefinitionChange.PendingCount != 1 || !item.CanDecide || !strings.HasSuffix(item.Href, "?definition_changes=1#definition-changes") {
				t.Fatalf("bad Desk source: %+v", item)
			}
		}
	}
	if count != 6 {
		t.Fatalf("later default change did not reopen six admin decisions: %+v", page)
	}
}
