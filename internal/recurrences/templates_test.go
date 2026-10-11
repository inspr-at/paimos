// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risks: duplicate schedules after lost/concurrent apply responses, accidental
// execution/consent and substituting an installer's rights for the current owner.
// Ordinary recurrence/definition tests already guard scheduling, release receipts
// and scope isolation. This exercises templates through those same HTTP guards.
func TestRoutineTemplatesValidateApplyWithoutActivationOrDuplicates(t *testing.T) {
	f := setup(t)
	reader := projectPrincipal(f, "viewer")
	policy := f.node("guideline", &f.project, "Accepted routine policy")
	source := f.node("project", nil, "Release source")
	tags := []string{f.node("tag", &f.project, "Routine fixture"), f.node("tag", nil, "Workspace fixture")}
	var kind string
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT aeon_seed_work_kinds($1)`, f.p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend' AND project_id IS NULL AND archived_at IS NULL`).Scan(&kind)
	})
	config := RoutineTemplateConfig{ProjectID: f.project, ParentID: f.parent, OwnerPrincipalID: f.p.ID, WorkKindID: kind, Sources: []SourceReference{{Kind: "knowledge", ID: policy}, {Kind: "node", ID: f.parent}}, Tags: tags}
	var catalogue struct {
		Items []RoutineTemplatePreset `json:"items"`
	}
	if err := json.Unmarshal(f.call(f.p, "GET", "/api/recurrences/templates", nil, 200), &catalogue); err != nil || len(catalogue.Items) != 3 {
		t.Fatalf("catalogue: %+v %v", catalogue, err)
	}
	unauthenticated := httptest.NewRecorder()
	f.handler.ServeHTTP(unauthenticated, httptest.NewRequest("GET", "/api/recurrences/templates", nil))
	if unauthenticated.Code != 401 {
		t.Fatalf("unauthenticated catalogue: %d", unauthenticated.Code)
	}
	for _, preset := range catalogue.Items {
		for _, language := range []string{"en", "de"} {
			t.Run(preset.ID+"/"+language, func(t *testing.T) {
				c := config
				c.Language = language
				if preset.ID == "website-release-update" {
					c.SourceProjectID = source
				}
				path := "/api/recurrences/templates/" + preset.ID + "/" + preset.Version
				var preview RoutineTemplatePreview
				if err := json.Unmarshal(f.call(f.p, "POST", path+"/preview", c, 200), &preview); err != nil {
					t.Fatal(err)
				}
				var ordinary struct {
					Times []string `json:"times"`
					Kind  string   `json:"trigger_kind"`
				}
				if err := json.Unmarshal(f.call(f.p, "POST", "/api/recurrences/preview", preview.Input, 200), &ordinary); err != nil {
					t.Fatal(err)
				}
				in := preview.Input
				if in.Definition.Scope != (DefinitionScope{Kind: "project", ProjectID: f.project}) || in.ParentID != f.parent || in.Definition.OwnerPrincipalID != f.p.ID ||
					in.Definition.Assignment.Role != preset.Role || in.Definition.Assignment.Budget.Mode != "off" || !in.Definition.Assignment.RuntimeRequirements.NeedsNativeHost ||
					in.QueueEach || in.OverlapPolicy != "skip" || in.CatchUpPolicy != "one" || in.Template.Type != "work" || in.Template.HideFromReleaseNotes == nil || !*in.Template.HideFromReleaseNotes {
					t.Fatalf("unsafe template projection: %+v", in)
				}
				if len(in.Template.Criteria) == 0 || in.Template.PillEN == "" || in.Template.PillDE == "" || in.Template.BenefitEN == "" || in.Template.BenefitDE == "" ||
					preview.Preview.ExecutionPolicy == nil || preview.Preview.ExecutionPolicy.Eligible || !preview.Preview.ExecutionPolicy.ConsentRequired {
					t.Fatalf("incomplete or executable preview: %+v", preview)
				}
				if preset.ID == "website-release-update" {
					if ordinary.Kind != "event" || len(ordinary.Times) != 0 || !reflect.DeepEqual(in.Trigger.Filter.ProjectIDs, []string{source}) || !in.Definition.Assignment.RuntimeRequirements.NeedsBrowser {
						t.Fatalf("website subscription: %+v", preview)
					}
				} else if ordinary.Kind != "time" || len(ordinary.Times) != 4 || len(preview.Preview.Times) != 4 || in.Trigger.Timezone != "Europe/Vienna" || in.Trigger.TimeOfDay != "09:00" {
					t.Fatalf("calendar preview: %+v", preview)
				}
				var first Recurrence
				if err := json.Unmarshal(f.call(f.p, "POST", path+"/apply", c, 201), &first); err != nil {
					t.Fatal(err)
				}
				if !first.Paused || first.Revision != 1 || first.OccurrenceCount != 0 || first.ExecutionPolicy == nil || first.ExecutionPolicy.Eligible {
					t.Fatalf("install activated routine: %+v", first)
				}
				// An omitted start date and UUID spelling must remain retry-safe
				// even after the clock moves beyond the original installation day.
				f.now = f.now.AddDate(0, 0, 8)
				c.ProjectID, c.ParentID, c.OwnerPrincipalID, c.WorkKindID = strings.ToUpper(c.ProjectID), strings.ToUpper(c.ParentID), strings.ToUpper(c.OwnerPrincipalID), strings.ToUpper(c.WorkKindID)
				c.Sources = []SourceReference{{Kind: "node", ID: strings.ToUpper(f.parent)}, {Kind: "knowledge", ID: strings.ToUpper(policy)}}
				c.Tags = []string{strings.ToUpper(tags[1]), strings.ToUpper(tags[0])}
				var retry Recurrence
				if err := json.Unmarshal(f.call(f.p, "POST", path+"/apply", c, 200), &retry); err != nil || retry.ID != first.ID || retry.Trigger.StartDate != first.Trigger.StartDate || retry.Revision != 1 {
					t.Fatalf("duplicate or changed retry: %+v %v", retry, err)
				}
				// Applying again must not overwrite an ordinary revisioned edit.
				edited := first.Input
				edited.Template.Description = "Person's inspected local edit"
				f.call(f.p, "PUT", "/api/recurrences/"+first.ID, struct {
					Input
					ExpectedRevision int64 `json:"expected_revision"`
				}{edited, first.Revision}, 200)
				if err := json.Unmarshal(f.call(f.p, "POST", path+"/apply", c, 200), &retry); err != nil || retry.Template.Description != edited.Template.Description || retry.Revision != 2 {
					t.Fatalf("apply overwrote current record: %+v %v", retry, err)
				}
				f.call(reader, "POST", path+"/apply", c, 403)
				f.call(reader, "POST", path+"/preview", c, 403)
			})
		}
	}
	f.run()
	f.tx(func(tx pgx.Tx) error {
		var inert bool
		err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM recurrences)=6
 AND (SELECT count(*) FROM events WHERE type='recurrence.created')=6
 AND NOT EXISTS(SELECT 1 FROM recurrences WHERE NOT paused)
 AND NOT EXISTS(SELECT 1 FROM agent_runs) AND NOT EXISTS(SELECT 1 FROM recurrence_occurrences)
 AND NOT EXISTS(SELECT 1 FROM routine_runs)
 AND NOT EXISTS(SELECT 1 FROM recurrence_definitions WHERE execute_consent OR consent_revision<>0 OR execution_principal_id IS NOT NULL OR consented_by_principal_id IS NOT NULL)`).Scan(&inert)
		if err == nil && !inert {
			t.Fatal("preview/apply duplicated schedules or granted execution")
		}
		return err
	})
	// Prove the first apply is still in its final transaction when a second
	// caller reaches the competing tenant fence. No sleeps establish overlap.
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return strings.Contains(sql, "INSERT INTO recurrences(") })
	m := New(pool)
	m.now = f.m.now
	f.install(m)
	concurrent := config
	concurrent.ParentID = f.node("work", &f.project, "Separate explicitly selected output")
	path := "/api/recurrences/templates/weekly-cleanup/1/apply"
	raw, err := json.Marshal(concurrent)
	if err != nil {
		t.Fatal(err)
	}
	call := func(done chan *httptest.ResponseRecorder) {
		req := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
		req = req.WithContext(tenant.WithPrincipal(ctx, f.p))
		out := httptest.NewRecorder()
		f.handler.ServeHTTP(out, req)
		done <- out
	}
	firstDone, retryDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go call(firstDone)
	pid := barrier.Wait(t, ctx)
	go call(retryDone)
	dbtest.WaitForLock(t, ctx, f.d, pid, "transactionid")
	barrier.Release()
	var installed Recurrence
	for i, done := range []chan *httptest.ResponseRecorder{firstDone, retryDone} {
		out := dbtest.Await(t, ctx, done)
		var got Recurrence
		if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != 201-i || !got.Paused {
			t.Fatalf("concurrent apply: %d %s %v", out.Code, out.Body.String(), err)
		}
		if i == 0 {
			installed = got
		} else if got.ID != installed.ID {
			t.Fatal("concurrent apply created another schedule")
		}
	}
	f.tx(func(tx pgx.Tx) error {
		var created int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='recurrence.created' AND after->>'id'=$1`, installed.ID).Scan(&created); err != nil {
			return err
		}
		if created != 1 {
			t.Fatalf("concurrent apply wrote %d creation events", created)
		}
		return nil
	})
	f.call(f.p, "DELETE", "/api/recurrences/"+installed.ID, map[string]int64{"expected_revision": installed.Revision}, 204)
	if raw := f.call(f.p, "POST", path, concurrent, 409); !strings.Contains(string(raw), "configuration was retired") {
		t.Fatalf("wrong retirement refusal: %s", raw)
	}
	badOwner := config
	badOwner.OwnerPrincipalID = reader.ID
	f.call(f.p, "POST", path, badOwner, 403)
	website := config
	website.SourceProjectID = f.project
	if raw := f.call(f.p, "POST", "/api/recurrences/templates/website-release-update/1/apply", website, 400); !strings.Contains(string(raw), "distinct source project") {
		t.Fatalf("wrong source refusal: %s", raw)
	}
	f.call(f.p, "POST", "/api/recurrences/templates/weekly-cleanup/2/apply", config, 404)
}
