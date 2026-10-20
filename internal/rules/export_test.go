// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: retiring the writer must neither leak private/foreign rows through the
// export nor alter a managed run's published rules, and no writer may bypass it.
func TestRulesExportAndAuthoringSwitch(t *testing.T) {
	w := newBatchWorld(t, "rules-export")
	admin := w.principal(tenant.Person, "owner", "admin")
	colleague := w.principal(tenant.Person, "colleague", "admin")
	member := w.principal(tenant.Person, "member", "member")
	company := w.layer(admin, Scope{Layer: "company"})
	public := w.set(admin, company, "Company", lockedRule("company", "Use reviewed changes."))
	privateRule := testRule("kernel", "Keep local context.")
	privateRule.Source.Reference = "AGENTS-KERNEL-PRIVATE.md"
	kernel := w.set(admin, company, "Operator", privateRule)
	project := w.set(admin, w.layer(admin, Scope{Layer: "project", ProjectID: w.project}), "Project", testRule("project", "Use typed SQL."))
	person := w.set(admin, w.layer(admin, Scope{Layer: "person", OwnerID: admin.ID}), "Person", testRule("person", "Explain the outcome first."))
	hidden := w.set(colleague, w.layer(colleague, Scope{Layer: "person", OwnerID: colleague.ID}), "Private", testRule("hidden", "Never export this colleague preference."))
	for _, s := range []Set{public, kernel, project, person} {
		w.publish(admin, s, "260928120000.0.0")
	}
	w.publish(colleague, hidden, "260928120000.0.0")
	w.publish(admin, public, "260928120001.0.0")
	var foreignID string
	if err := w.d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('rules-export-foreign','Foreign') RETURNING id::text`).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}
	other := &batchWorld{t: t, d: w.d, tid: foreignID, mux: w.mux}
	foreign := other.principal(tenant.Person, "foreign owner", "admin")
	foreignSet := other.set(foreign, other.layer(foreign, Scope{Layer: "company"}), "Foreign", testRule("foreign", "Never export another tenant."))
	other.publish(foreign, foreignSet, "260928120000.0.0")
	if _, err := w.d.Admin.Exec(t.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,'sample/project','ci.yml','nightly.yml',clock_timestamp())`, w.tid, w.project); err != nil {
		t.Fatal(err)
	}
	w.call(member, "GET", "/api/rules/export", nil, 403)
	first := w.call(admin, "GET", "/api/rules/export", nil, 200)
	second := w.call(admin, "GET", "/api/rules/export", nil, 200)
	if !bytes.Equal(first, second) {
		t.Fatal("export changed on an unchanged rerun")
	}
	if bytes.Contains(first, []byte(hidden.ID)) || bytes.Contains(first, []byte(foreignSet.ID)) || bytes.Contains(first, []byte("Never export")) {
		t.Fatal("export leaked private or foreign prose")
	}
	var auditRaw []byte
	if err := w.d.Admin.QueryRow(t.Context(), `SELECT after FROM events WHERE tenant_id=$1 AND type='settings.rules_exported' ORDER BY id DESC LIMIT 1`, w.tid).Scan(&auditRaw); err != nil {
		t.Fatal(err)
	}
	var audit map[string]any
	if err := json.Unmarshal(auditRaw, &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit) != 2 || audit["layers"] != float64(3) || audit["sha256"] != exportSHA(strings.TrimSpace(string(first))) {
		t.Fatalf("export audit is not the value-free count/digest receipt: %v", audit)
	}
	var bundle ExportBundle
	if err := json.Unmarshal(first, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != 3 || bundle.Manifest.TenantID != w.tid {
		t.Fatalf("bundle %+v", bundle.Manifest)
	}
	versions := 0
	privateTargets := 0
	pending := 0
	for _, f := range bundle.Files {
		if exportSHA(f.Content) != f.SHA256 {
			t.Fatal("file digest differs")
		}
	}
	for _, l := range bundle.Manifest.Layers {
		for _, s := range l.Sets {
			for _, v := range s.Versions {
				versions++
				if v.Snapshot.SHA256 != SnapshotDigest(v.Snapshot) || len(v.Authors) != 1 || v.Authors[0] != admin.ID {
					t.Fatalf("lost version digest/author: %+v", v)
				}
				for i, text := range v.Texts {
					r := v.Snapshot.Rules[i]
					if text.SHA256 != exportSHA(r.Text) || text.WhySHA256 != exportSHA(r.Why) || text.DetailsSHA256 != exportSHA(r.Details) {
						t.Fatal("text digest differs")
					}
					if text.Target.Repository == "inspr-at/inspr-doctrine-private" {
						privateTargets++
					}
					if text.Target.Target == "needs decision" {
						pending++
					}
				}
			}
		}
	}
	for _, l := range bundle.Manifest.Layers {
		if l.Scope.Layer == "project" && (l.Target.Repository != "sample/project" || l.Target.Path != "AGENTS.md") {
			t.Fatalf("project target not resolved: %+v", l.Target)
		}
	}
	if versions != 5 || privateTargets != 1 || pending != 1 {
		t.Fatalf("versions=%d private=%d pending=%d", versions, privateTargets, pending)
	}
	agent := w.principal(tenant.Agent, "managed builder", "admin")
	agent.KeyCreatorID = admin.ID
	managed := func() Merged {
		var got Merged
		err := db.InTenant(tenant.WithPrincipal(t.Context(), agent), w.d.App, w.tid, func(tx pgx.Tx) error {
			var err error
			got, err = ForManagedSession(t.Context(), tx, agent, w.project, "", "codex", MaxBytes)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := managed()
	golden := "Use reviewed changes."
	if !strings.Contains(before.Body, golden) {
		t.Fatalf("fixture missing expected published text: %s", before.Body)
	}
	w.call(member, "PUT", "/api/rules/authoring", map[string]bool{"db_authoring": false}, 403)
	w.call(admin, "PUT", "/api/rules/authoring", map[string]bool{"db_authoring": false}, 200)
	if after := managed(); !reflect.DeepEqual(before, after) {
		t.Fatal("managed delivery changed when authoring was disabled")
	}
	path := "/api/rules/sets/" + public.ID
	for _, request := range []struct{ method, path string }{
		{"POST", "/api/rules/layers"}, {"POST", "/api/rules/sets"}, {"PUT", path + "/draft"},
		{"POST", path + "/publish"}, {"POST", path + "/restore"}, {"PUT", path + "/tldr"}, {"POST", "/api/rules/publish"},
	} {
		raw := w.call(admin, request.method, request.path, map[string]any{}, 409)
		var refusal Error
		if err := json.Unmarshal(raw, &refusal); err != nil {
			t.Fatal(err)
		}
		if refusal.Code != DBAuthoringDisabled || !strings.Contains(refusal.Message, "/api/rules/doctrine/inbox") {
			t.Fatalf("wrong refusal: %s", raw)
		}
	}
	var set Set
	if err := json.Unmarshal(w.call(admin, "GET", path, nil, 200), &set); err != nil {
		t.Fatal(err)
	}
	if set.DBAuthoring == nil || *set.DBAuthoring {
		t.Fatal("CLI read did not report disabled authoring")
	}
	if got := w.call(admin, "GET", "/api/rules/export", nil, 200); !bytes.Equal(first, got) {
		t.Fatal("switch changed published export")
	}
	w.call(admin, "PUT", "/api/rules/authoring", map[string]bool{"db_authoring": true}, 200)
	w.call(admin, "PUT", path+"/draft", draftInput{ExpectedRevision: public.Revision, Name: public.Name, Rules: public.Rules}, 200)
}
