// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risks: tenant/person leakage, owner authority substitution, stale edits and
// accidental execution. Existing recurrence guards cover legacy receipt/queue
// idempotency; this test exercises the new scope boundary through real RLS.
func TestRoutineDefinitionScopeAuthorityAndInertRoundTrip(t *testing.T) {
	f := setup(t)
	reader := projectPrincipal(f, "viewer")
	member := projectPrincipal(f, "member")
	workspaceReader := viewer(f)
	var kind string
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT aeon_seed_work_kinds($1)`, f.p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend' AND archived_at IS NULL AND project_id IS NULL`).Scan(&kind)
	})
	tokens, money := int64(10000), int64(2000000)
	in := f.input()
	in.QueueEach = true
	in.Template.Name = "Private routine name"
	in.Definition = &Definition{
		Scope: DefinitionScope{Kind: "personal"}, OwnerPrincipalID: f.p.ID,
		Assignment: &Assignment{Goal: "Review bounded sources", Sources: []SourceReference{{Kind: "node", ID: f.parent}},
			Role: "build", WorkKindID: kind, AllowedActions: []string{"work.create", "pipeline.request"},
			RuntimeRequirements: RuntimeRequirements{NeedsNativeHost: true, NeedsBrowser: true, RuntimeClass: "native_browser"},
			Budget:              DefinitionBudget{Mode: "both", TokenCeiling: &tokens, MoneyCeilingMicroUSD: &money}},
	}
	private := f.create(in)
	if !private.Paused || !reflect.DeepEqual(private.Definition, in.Definition) {
		t.Fatalf("saved definition changed or became active: %+v", private)
	}
	if got := f.get(private.ID); !reflect.DeepEqual(got.Definition, in.Definition) {
		t.Fatalf("get lost assignment: %+v", got.Definition)
	}
	f.tx(func(tx pgx.Tx) error {
		// An older released server does not know the optional projection fields.
		// Its audit snapshots must still follow the saved private scope.
		oldSnapshot := private
		oldSnapshot.Definition = nil
		return record(t.Context(), tx, f.p, private.ProjectID, "recurrence.updated", private, oldSnapshot)
	})
	f.now = f.now.AddDate(0, 0, 30)
	f.run()
	f.tx(func(tx pgx.Tx) error {
		var inert bool
		err := tx.QueryRow(t.Context(), `SELECT NOT EXISTS(SELECT 1 FROM agent_runs) AND NOT EXISTS(SELECT 1 FROM recurrence_occurrences)
 AND NOT EXISTS(SELECT 1 FROM recurrence_definitions WHERE execute_consent OR consent_revision<>0 OR execution_principal_id IS NOT NULL OR consented_by_principal_id IS NOT NULL)`).Scan(&inert)
		if err == nil && !inert {
			t.Fatal("saving definition granted execution or created work")
		}
		return err
	})
	for _, p := range []tenant.Principal{reader, workspaceReader} {
		for _, suffix := range []string{"", "/history", "/preview", "/releases"} {
			f.call(p, "GET", "/api/recurrences/"+private.ID+suffix, nil, 404)
		}
		f.call(p, "POST", "/api/recurrences/"+private.ID+"/run-now", map[string]string{"idempotency_key": "private"}, 404)
		ctx := tenant.WithPrincipal(t.Context(), p)
		if err := db.InTenant(ctx, f.d.App, p.TenantID, func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE after->>'id'=$1 OR before->>'id'=$1`, private.ID).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("private definition leaked through tenant audit events")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	edit := func(input Input, revision int64, status int) {
		f.call(f.p, "PUT", "/api/recurrences/"+private.ID, struct {
			Input
			ExpectedRevision int64 `json:"expected_revision"`
		}{input, revision}, status)
	}
	// An old editor sends only legacy fields. The full new assignment survives.
	oldEdit := private.Input
	oldEdit.Definition = nil
	oldEdit.Template.Description = "Legacy editor changed only the description"
	edit(oldEdit, 1, 200)
	edit(oldEdit, 1, 409)
	current := f.get(private.ID)
	if current.Revision != 2 || !reflect.DeepEqual(current.Definition, in.Definition) || !current.Paused {
		t.Fatalf("legacy PUT lost private definition: %+v", current)
	}
	changed := current.Input
	d := *current.Definition
	a := *d.Assignment
	a.Goal = "New revision-bound goal"
	d.Assignment = &a
	changed.Definition = &d
	edit(changed, 2, 200)
	if got := f.get(private.ID); !reflect.DeepEqual(got.Definition, changed.Definition) || got.Revision != 3 {
		t.Fatalf("new PUT did not round-trip: %+v", got)
	}
	d.Scope = DefinitionScope{Kind: "workspace"}
	edit(changed, 3, 400)
	d.Scope = DefinitionScope{Kind: "personal"}
	// Personal/workspace scope cannot borrow the saving owner's target rights.
	otherProject := f.node("project", nil, "Inaccessible output")
	otherParent := f.node("work", &otherProject, "Other parent")
	denied := f.input()
	denied.ProjectID, denied.ParentID = otherProject, otherParent
	denied.Definition = &Definition{Scope: DefinitionScope{Kind: "personal"}, OwnerPrincipalID: member.ID}
	f.call(member, "POST", "/api/recurrences", denied, 403)
	projectAdmin := projectPrincipal(f, "admin")
	denied.Definition = &Definition{Scope: DefinitionScope{Kind: "workspace"}, OwnerPrincipalID: projectAdmin.ID}
	f.call(f.p, "POST", "/api/recurrences", denied, 403)
	denied.Definition = &Definition{Scope: DefinitionScope{Kind: "project", ProjectID: f.project}, OwnerPrincipalID: projectAdmin.ID}
	f.call(f.p, "POST", "/api/recurrences", denied, 403)
	denied.Definition = &Definition{Scope: DefinitionScope{Kind: "personal"}, OwnerPrincipalID: f.p.ID}
	f.call(member, "POST", "/api/recurrences/preview", denied, 403)
	// An active canonical person in another tenant cannot own the projection.
	const foreignTenant = "20000000-0000-4000-8000-000000000001"
	var foreignOwner string
	if err := db.InTenant(db.AllProjects(t.Context(), "routine tenant fixture"), f.d.App, foreignTenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'foreign-routine','Foreign routine')`, foreignTenant); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Foreign owner') RETURNING id::text`, foreignTenant).Scan(&foreignOwner)
	}); err != nil {
		t.Fatal(err)
	}
	denied = f.input()
	denied.Definition = &Definition{Scope: DefinitionScope{Kind: "project", ProjectID: f.project}, OwnerPrincipalID: foreignOwner}
	f.call(f.p, "POST", "/api/recurrences", denied, 404)
	foreign := tenant.Principal{ID: foreignOwner, TenantID: foreignTenant, Kind: tenant.Person}
	f.call(foreign, "GET", "/api/recurrences/"+private.ID, nil, 404)
	// Definition project and output project are deliberately independent.
	projectInput := f.input()
	projectInput.Definition = &Definition{Scope: DefinitionScope{Kind: "project", ProjectID: f.project}, OwnerPrincipalID: f.p.ID}
	project := f.create(projectInput)
	f.call(reader, "GET", "/api/recurrences/"+project.ID, nil, 200)
	projectInput.Definition.Scope.ProjectID = otherProject
	separate := f.create(projectInput)
	f.call(reader, "GET", "/api/recurrences/"+separate.ID, nil, 404)
	workspaceInput := f.input()
	workspaceInput.Definition = &Definition{Scope: DefinitionScope{Kind: "workspace"}, OwnerPrincipalID: f.p.ID}
	workspace := f.create(workspaceInput)
	f.call(workspaceReader, "GET", "/api/recurrences/"+workspace.ID, nil, 200)
	f.call(reader, "GET", "/api/recurrences/"+workspace.ID, nil, 404)
	legacy := f.create(f.input())
	if legacy.Paused || legacy.Definition != nil {
		t.Fatal("legacy definition behavior changed")
	}
	f.manual(legacy.ID, "legacy-still-inert")
	f.tx(func(tx pgx.Tx) error {
		// Seed hidden names ahead of visible rows. Filtering after LIMIT would
		// produce an empty/short page instead of the full visible 100-row page.
		_, err := tx.Exec(t.Context(), `INSERT INTO recurrences(tenant_id,id,project_id,parent_id,template,trigger,next_at,created_by_principal_id)
 SELECT tenant_id,format('00000000-0000-4000-8000-%s',lpad(i::text,12,'0'))::uuid,project_id,parent_id,template,trigger,next_at,created_by_principal_id
 FROM recurrences CROSS JOIN generate_series(1,105) i WHERE id=$1`, private.ID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `INSERT INTO recurrence_definitions(tenant_id,recurrence_id,scope_type,owner_principal_id,output_project_id,output_parent_id)
 SELECT tenant_id,id,'personal',created_by_principal_id,project_id,parent_id FROM recurrences WHERE id::text LIKE '00000000-0000-4000-8000-%'`); err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `UPDATE recurrences SET definition_scope='personal' WHERE id::text LIKE '00000000-0000-4000-8000-%'`); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO recurrences(tenant_id,id,project_id,parent_id,template,trigger,next_at,created_by_principal_id)
 SELECT tenant_id,format('10000000-0000-4000-8000-%s',lpad(i::text,12,'0'))::uuid,project_id,parent_id,template,trigger,next_at,created_by_principal_id
 FROM recurrences CROSS JOIN generate_series(1,101) i WHERE id=$1`, legacy.ID)
		return err
	})
	var page struct {
		Items []Recurrence `json:"items"`
		Next  *string      `json:"next_cursor"`
	}
	parsePage := func(path string, p tenant.Principal) {
		t.Helper()
		if err := json.Unmarshal(f.call(p, "GET", path, nil, 200), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.Template.Name == "Private routine name" || item.ID == workspace.ID || item.ID == separate.ID {
				t.Fatal("list leaked an inaccessible definition")
			}
		}
	}
	parsePage("/api/recurrences?scope=project", reader)
	if len(page.Items) != 100 || page.Next == nil {
		t.Fatalf("bounded visible page = %d, cursor %v", len(page.Items), page.Next)
	}
	firstCursor := *page.Next
	parsePage("/api/recurrences?scope=project&after="+firstCursor, reader)
	if len(page.Items) != 3 || page.Next != nil {
		t.Fatalf("next visible page = %d, cursor %v", len(page.Items), page.Next)
	}
	parsePage("/api/recurrences?scope=personal", reader)
	if len(page.Items) != 0 || page.Next != nil {
		t.Fatal("private scope yielded items or a hidden cursor")
	}
	// Unknown outward effects and impossible ceilings are rejected before save.
	invalid := in
	copyDef, copyAssignment := *in.Definition, *in.Definition.Assignment
	invalid.Definition = &copyDef
	copyDef.Assignment = &copyAssignment
	copyAssignment.AllowedActions = []string{"survey.send"}
	f.call(f.p, "POST", "/api/recurrences", invalid, 400)
	copyAssignment.AllowedActions = []string{}
	zero := int64(0)
	copyAssignment.Budget.TokenCeiling = &zero
	f.call(f.p, "POST", "/api/recurrences", invalid, 400)
	copyAssignment.Budget.TokenCeiling = &tokens
	copyAssignment.Sources = append(copyAssignment.Sources, copyAssignment.Sources[0])
	f.call(f.p, "POST", "/api/recurrences", invalid, 400)
	if !strings.Contains(string(f.call(f.p, "GET", "/api/recurrences/"+private.ID+"/history", nil, 200)), "New revision-bound goal") {
		t.Fatal("authorized history lost definition edit")
	}
}
