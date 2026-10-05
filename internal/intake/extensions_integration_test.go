// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestDraftExtensionsPersistWithIsolationAndAcceptance(t *testing.T) {
	database := dbtest.Open(t)
	fx := newFixture(t, database)
	mux := http.NewServeMux()
	New(database.App).Mount(mux)
	grantIntake(t, database.App, fx.tenantA, fx.agent, fx.person, fx.projectA)
	var source sourceView
	decodeJSON(t, fx.post(t, mux, fx.person, "", "/intake/sources", sourceBody("note", "Evidence", "extensions-source", "")), &source)
	raw := json.RawMessage(`{ "x-demo.constraints@1": {"version":"1.2","data":{"quote":"  e\u0301\r\n<script>verbatim</script>  ","precision":1.00,"control":"\u0000"}},"x-demo.constraints@2":{"version":"2.0","data":null} }`)
	document := reviewDocument(raw)
	request := fx.brief(source.ID, "", 0, "extensions-draft", "Extended brief", "Native body")
	request = request[:len(request)-1] + `,"document_bytes":` + jsonString(document) + `}`
	draft := fx.mustDraft(t, mux, request)
	if draft.DocumentBytes == nil || *draft.DocumentBytes != document {
		t.Fatal("proposal did not return the original document")
	}
	var storedMap, storedDocument string
	if err := database.Admin.QueryRow(t.Context(), `SELECT extensions, document_bytes FROM intake_drafts WHERE id=$1`, draft.ID).Scan(&storedMap, &storedDocument); err != nil {
		t.Fatal(err)
	}
	if storedMap != string(raw) || storedDocument != document {
		t.Fatal("database reshaped the map or changed document bytes")
	}
	var evidence map[string]struct{ Data struct{ Quote string } }
	if err := json.Unmarshal(draft.Extensions, &evidence); err != nil || evidence["x-demo.constraints@1"].Data.Quote != "  e\u0301\r\n<script>verbatim</script>  " {
		t.Fatalf("evidence changed: %v", err)
	}
	retry := fx.mustDraft(t, mux, request)
	if retry.ID != draft.ID {
		t.Fatal("retry created another draft")
	}
	changed := strings.Replace(request, "precision", "changed", 1)
	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/drafts", changed); w.Code != http.StatusConflict {
		t.Fatalf("changed retry: %d", w.Code)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), database.App, fx.tenantB, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM intake_drafts WHERE id=$1`, draft.ID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Error("other tenant read the extension draft")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := call(mux, fx.personB, "", http.MethodGet, "/api/projects/"+fx.projectA+"/intake", ""); w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant snapshot: %d", w.Code)
	}
	assertPayloadOmits(t, database, fx.tenantA, evDraftProposed, "precision")
	assertPayloadOmits(t, database, fx.tenantA, evDraftProposed, "document_bytes")
	if _, err := database.Admin.Exec(t.Context(), `UPDATE intake_drafts SET extensions='{}' WHERE id=$1`, draft.ID); err == nil {
		t.Fatal("draft extensions can be mutated")
	}
	accepted := fx.post(t, mux, fx.person, "", "/intake/drafts/"+draft.ID+"/accept", `{"expected_base_event_id":0}`)
	if accepted.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", accepted.Code, accepted.Body.String())
	}
	var view draftView
	decodeJSON(t, accepted, &view)
	if view.DocumentBytes == nil || *view.DocumentBytes != document || !bytes.Equal(view.Extensions, draft.Extensions) {
		t.Fatal("acceptance changed extension data")
	}
	var filtered snapshot
	decodeJSON(t, fx.get(t, mux, fx.person, "?node_id="+*view.TargetNodeID), &filtered)
	if len(filtered.Drafts) != 1 || filtered.Drafts[0].ID != draft.ID || len(filtered.Sources) != 0 || len(filtered.Turns) != 0 {
		t.Fatal("accepted-node filtering lost the draft or returned unrelated evidence")
	}
	if w := fx.get(t, mux, fx.person, "?node_id=invalid"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid filter: %d", w.Code)
	}
	if w := fx.get(t, mux, fx.person, "?node_id="+fx.projectB); w.Code != http.StatusConflict {
		t.Fatalf("cross-project filter: %d", w.Code)
	}
	if w := call(mux, fx.agent, fx.writeToken, http.MethodGet, "/api/projects/"+fx.projectA+"/intake?node_id="+*view.TargetNodeID, ""); w.Code != http.StatusForbidden {
		t.Fatalf("unscoped agent filter: %d", w.Code)
	}
	// The native endpoint persists eight Aithema-valid instances at exactly the
	// full 64 KiB map boundary, with snapshot/projection overhead above 65,536.
	boundary := reviewDocument(extensionLimitMap(t))
	request = fx.brief(source.ID, "", 0, "extensions-limit", "Limit", "Boundary body")
	request = request[:len(request)-1] + `,"document_bytes":` + jsonString(boundary) + `}`
	if len(request) <= 65536 {
		t.Fatal("boundary request does not include enough overhead")
	}
	limitDraft := fx.mustDraft(t, mux, request)
	if len(limitDraft.Extensions) != 64<<10 || *limitDraft.DocumentBytes != boundary {
		t.Fatal("boundary extension map was lost")
	}
	// Native callers can supply only the map; no snapshot or extensions remain
	// mandatory for existing callers.
	request = fx.brief(source.ID, "", 0, "extensions-direct", "Direct map", "Body")
	request = request[:len(request)-1] + `,"extensions":` + string(raw) + `}`
	if direct := fx.mustDraft(t, mux, request); direct.DocumentBytes != nil || len(direct.Extensions) == 0 {
		t.Fatal("direct extension map was not retained")
	}
	legacy := fx.mustDraft(t, mux, fx.brief(source.ID, "", 0, "extensions-legacy", "Legacy", "Body"))
	if len(legacy.Extensions) != 0 || legacy.DocumentBytes != nil {
		t.Fatal("legacy draft gained extension fields")
	}
	var snap snapshot
	decodeJSON(t, fx.get(t, mux, fx.person, ""), &snap)
	if len(snap.Drafts) != 4 {
		t.Fatalf("readback draft count: %d", len(snap.Drafts))
	}
	found := false
	for _, entry := range snap.Drafts {
		found = found || entry.ID == draft.ID && entry.DocumentBytes != nil && *entry.DocumentBytes == document
	}
	if !found {
		t.Fatal("readback lost the original document")
	}
}

func TestGeneratedTicketReadsGeneratingRequirementExtensions(t *testing.T) {
	database := dbtest.Open(t)
	fx := newFixture(t, database)
	mux := http.NewServeMux()
	New(database.App).Mount(mux)
	grantIntake(t, database.App, fx.tenantA, fx.agent, fx.person, fx.projectA)
	var source sourceView
	decodeJSON(t, fx.post(t, mux, fx.person, "", "/intake/sources", sourceBody("note", "Evidence", "ticket-extensions-source", "")), &source)
	request := fx.requirement(source.ID, "ticket-extensions-draft")
	request = request[:len(request)-1] + `,"extensions":{"x-demo.readiness@1":{"version":"1.0","data":{"score":0.8}}}}`
	draft := fx.mustDraft(t, mux, request)
	var accepted draftView
	w := fx.post(t, mux, fx.person, "", "/intake/drafts/"+draft.ID+"/accept", `{"expected_base_event_id":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("accept requirement: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &accepted)
	feature, _ := fx.nodeWithEvent(t, database, "work", "EPIC-1", "Feature", "", fx.person.ID)
	ticket, _ := fx.nodeWithEvent(t, database, "work", "TKT-1", "Generated ticket", "", fx.person.ID)
	if _, err := database.Admin.Exec(t.Context(), `INSERT INTO journey_features (tenant_id, feature_node_id, project_node_id, requirement_node_id) VALUES ($1,$2,$3,$4)`, fx.tenantA, feature, fx.projectA, *accepted.TargetNodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Admin.Exec(t.Context(), `INSERT INTO journey_tickets (tenant_id, ticket_node_id, project_node_id, feature_node_id, walker_position, source) VALUES ($1,$2,$3,$4,0,'requirements')`, fx.tenantA, ticket, fx.projectA, feature); err != nil {
		t.Fatal(err)
	}
	var filtered snapshot
	decodeJSON(t, fx.get(t, mux, fx.person, "?node_id="+ticket), &filtered)
	if len(filtered.Drafts) != 1 || filtered.Drafts[0].ID != draft.ID || !bytes.Equal(filtered.Drafts[0].Extensions, accepted.Extensions) {
		t.Fatal("generated ticket lost generating requirement data")
	}
	if _, err := database.Admin.Exec(t.Context(), `UPDATE journey_tickets SET source='manual' WHERE ticket_node_id=$1`, ticket); err != nil {
		t.Fatal(err)
	}
	decodeJSON(t, fx.get(t, mux, fx.person, "?node_id="+ticket), &filtered)
	if len(filtered.Drafts) != 0 {
		t.Fatal("manually associated ticket inherited analysis it was not generated from")
	}
}
