// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-274: a nil list or map in a profile definition must never be stored as
// null, and stored JSON with null lists must be refused.
func TestProfileDefinitionNeverEncodesNullLists(t *testing.T) {
	raw, err := json.Marshal(profileDefinition{Schema: "inspr.document-profile.v1"}.normalized())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"fonts":[]`, `"colors":{}`, `"typography":{}`, `"cover":{}`, `"sections":{}`, `"labels":{}`, `"columns":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
	if strings.Contains(string(raw), "null") {
		t.Fatalf("null in %s", raw)
	}
	if err := checkProfileJSON("definition", raw); err != nil {
		t.Fatal(err)
	}
	// Also inside a document snapshot.
	snapshot, err := json.Marshal(normalizedSnapshot(&documentProfileSnapshot{ID: "x", Revision: 1}))
	if err != nil || !strings.Contains(string(snapshot), `"fonts":[]`) {
		t.Fatalf("snapshot: %s %v", snapshot, err)
	}
	var def map[string]any
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatal(err)
	}
	def["fonts"] = nil
	broken, _ := json.Marshal(def)
	if err := checkProfileJSON("definition", broken); err == nil || !strings.Contains(err.Error(), "fonts") {
		t.Fatalf("null fonts accepted: %v", err)
	}
	stored, err := marshalProfile(profileDefinition{})
	if err != nil || strings.Contains(string(stored), "null") {
		t.Fatalf("marshalProfile must normalise: %s %v", stored, err)
	}
}

func TestMarshalDraftWritesEmptyLists(t *testing.T) {
	doc := quoteDocument{SchemaVersion: 1, MinimumWriterVersion: 1, Sender: json.RawMessage(`{}`), Recipient: json.RawMessage(`{}`), Legal: json.RawMessage(`{}`), Layout: json.RawMessage(`{}`)}
	doc.Sections = []documentSection{{ID: "11111111-1111-4111-8111-111111111111", Heading: "Scope"}}
	doc.Profile = &documentProfileSnapshot{ID: "11111111-1111-4111-8111-111111111111", Revision: 1, Definition: profileDefinition{Schema: "inspr.document-profile.v1"}}
	// A frozen version is never rewritten (its digest covers it): refused.
	if _, err := marshalDocument(doc); err == nil {
		t.Fatal("marshalDocument accepted null lists")
	}
	raw, err := marshalDraft(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"positions":[]`, `"nodes":[]`, `"fonts":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
	if doc.Sections[0].Nodes != nil || doc.Profile.Definition.Fonts != nil {
		t.Fatal("marshalDraft changed the caller's sections")
	}
	if err := CheckLoadableDocument(raw); err != nil {
		t.Fatalf("normalised document not loadable: %v", err)
	}
	for _, broken := range []string{
		strings.Replace(string(raw), `"fonts":[]`, `"fonts":null`, 1),
		strings.Replace(string(raw), `"labels":{}`, `"labels":null`, 1),
		strings.Replace(string(raw), `"positions":[]`, `"positions":null`, 1),
		strings.Replace(string(raw), `"nodes":[]`, `"nodes":null`, 1),
	} {
		if err := CheckLoadableDocument([]byte(broken)); err == nil {
			t.Errorf("stored null reported loadable: %s", broken)
		}
	}
}

// Review of AEON-274: a draft or an issued snapshot stored before the fix can
// hold null profile lists. Issuing such a draft, duplicating such a version and
// issuing the copy must all work, and every stored draft must be loadable.
func TestNullProfileListsStayIssuableThroughDuplicate(t *testing.T) {
	f := newQuoteFixture(t, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "quotes-null-lists")
	ctx := context.Background()
	settings := `{"expected_revision":0,"numbering_time_zone":"Europe/Vienna","default_currency":"EUR","sender":{"company":"Example Sender","street":"Example Street 1","postal_code":"0000","city":"Example City","country":"AT","email":"sender@example.test"},"defaults":{"intro":"","blocks":[],"accept_text":"","vat_note":""},"layout":{},"smtp_confirmation_enabled":false}`
	if status, body := f.call("admin", "PATCH", "/api/quotes/settings", settings); status != 200 {
		t.Fatalf("settings %d %v", status, body)
	}
	status, created := f.call("admin", "POST", "/api/quotes", fmt.Sprintf(`{"title":"Legacy profile","customer_org_node_id":%q}`, f.ids["org"]))
	if status != 201 {
		t.Fatalf("create %d %v", status, created)
	}
	id := created["quote_node_id"].(string)
	exec := func(stmts ...string) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(ctx), f.database.App, f.tenantID, func(tx pgx.Tx) error {
			for _, stmt := range stmts {
				if _, err := tx.Exec(ctx, stmt); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A profile snapshot as the bundle writer stored it before the fix.
	legacy := `{"id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","revision":1,"definition":{"schema":"inspr.document-profile.v1","layout_variant":"standard","locale":"en","fonts":null,"colors":{"ink":"#253335","muted":"#637477","soft":"#91a1a3","accent":"#287f78","rule":"#d5dfdf","paper":"#ffffff"},"typography":{"body_pt":"10"},"page":{"width_mm":"210","height_mm":"297","top_mm":"18","right_mm":"20","bottom_mm":"16","left_mm":"22"},"cover":null,"sections":null,"positions_table":{"columns":[],"separator":"rule","repeat_header":true},"totals":{"vat":"line","discount":"line","net_label":"Net"},"payment_terms":{"position":"after-totals","heading":"Payment terms"},"acceptance":{"signature_columns":1,"gap_mm":"14","lead_mm":"28"},"footer":{"width_mm":"33","offset_mm":"0","page_number_format":"PAGE {page} OF {total}"},"labels":null}}`
	position := `[{"id":"33333333-3333-4333-8333-333333333333","pricing_source":"manual","short_text":"Work","long_text":"","quantity":"1","unit_label":"hour","unit_price_cents":10000,"total_cents":10000,"currency":"EUR"}]`
	exec(fmt.Sprintf(`UPDATE quote_drafts SET document=jsonb_set(jsonb_set(document,'{profile}',%s::jsonb),'{positions}',%s::jsonb) || jsonb_build_object('recipient',document->'recipient' || '{"address":"Example Address","email":"customer@example.test"}'::jsonb) WHERE quote_node_id='%s'`, pgQuote(legacy), pgQuote(position), id))
	draft := func(qid string) map[string]any {
		t.Helper()
		status, d := f.call("admin", "GET", "/api/quotes/"+qid+"/draft", "")
		if status != 200 {
			t.Fatalf("draft %d %v", status, d)
		}
		return d
	}
	issue := func(qid string) {
		t.Helper()
		d := draft(qid)
		_, q := f.call("admin", "GET", "/api/quotes/"+qid, "")
		body := fmt.Sprintf(`{"expected_quote_revision":%s,"expected_draft_revision":%s,"expected_document_sha256":%q}`, q["revision"], d["draft_revision"], d["document_sha256"])
		if status, out := f.call("admin", "POST", "/api/quotes/"+qid+"/finalize", body); status != 200 {
			t.Fatalf("finalize %s %d %v", qid, status, out)
		}
	}
	// A draft stored with null lists is issued; its version is frozen normalised.
	issue(id)
	status, v1 := f.call("admin", "GET", "/api/quotes/"+id+"/versions/1", "")
	if status != 200 {
		t.Fatalf("version %d %v", status, v1)
	}
	frozen, _ := json.Marshal(v1["document"])
	if err := CheckLoadableDocument(frozen); err != nil {
		t.Fatalf("issued version: %v", err)
	}
	// An issued snapshot from before the fix still holds the nulls.
	for _, stmt := range []string{`ALTER TABLE quote_version_snapshots DISABLE TRIGGER quote_version_snapshots_immutable`,
		fmt.Sprintf(`UPDATE quote_version_snapshots SET document=jsonb_set(document,'{profile}',%s::jsonb) WHERE quote_node_id='%s' AND version=1`, pgQuote(legacy), id),
		`ALTER TABLE quote_version_snapshots ENABLE TRIGGER quote_version_snapshots_immutable`} {
		if _, err := f.database.Admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	_, q := f.call("admin", "GET", "/api/quotes/"+id, "")
	status, copyQ := f.call("admin", "POST", "/api/quotes/"+id+"/duplicate", fmt.Sprintf(`{"expected_revision":%s}`, revisionOf(q)))
	if status != 201 {
		t.Fatalf("duplicate %d %v", status, copyQ)
	}
	copyID := copyQ["quote_node_id"].(string)
	copied, _ := json.Marshal(draft(copyID)["document"])
	if err := CheckLoadableDocument(copied); err != nil {
		t.Fatalf("duplicated draft: %v", err)
	}
	// The copy takes the customer's current contact, which has no e-mail in
	// this fixture; completing it is ordinary editing, unrelated to profiles.
	exec(fmt.Sprintf(`UPDATE quote_drafts SET document=document || jsonb_build_object('recipient',document->'recipient' || '{"address":"Example Address","email":"customer@example.test"}'::jsonb) WHERE quote_node_id='%s'`, copyID))
	issue(copyID)

	// An ordinary save of a draft still holding null lists normalises it; the
	// client's echo of the null snapshot is the same profile, not a change.
	status, other := f.call("admin", "POST", "/api/quotes", fmt.Sprintf(`{"title":"Saved legacy","customer_org_node_id":%q}`, f.ids["org"]))
	if status != 201 {
		t.Fatalf("create %d %v", status, other)
	}
	otherID := other["quote_node_id"].(string)
	exec(fmt.Sprintf(`UPDATE quote_drafts SET document=jsonb_set(document,'{profile}',%s::jsonb) WHERE quote_node_id='%s'`, pgQuote(legacy), otherID))
	d := draft(otherID)
	doc := d["document"].(map[string]any)
	doc["title"] = "Saved legacy, edited"
	body, _ := json.Marshal(map[string]any{"client_session_id": "66666666-6666-4666-8666-666666666666", "mutation_id": "77777777-7777-4777-8777-777777777777", "writer_version": 2, "document": doc})
	req := httptest.NewRequest("PATCH", "/api/quotes/"+otherID+"/draft", strings.NewReader(string(body)))
	req.Header.Set("If-Match", fmt.Sprintf(`"qd-%s"`, d["draft_revision"]))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: f.ids["admin"], TenantID: f.tenantID, Kind: tenant.Person, Roles: []string{"admin"}}))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("save %d %s", rec.Code, rec.Body.String())
	}
	saved, _ := json.Marshal(draft(otherID)["document"])
	if err := CheckLoadableDocument(saved); err != nil {
		t.Fatalf("saved draft: %v", err)
	}
}

func pgQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
