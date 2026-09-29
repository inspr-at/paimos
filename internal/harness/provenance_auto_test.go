// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestReceiptRecordsRuleSourcesWithoutContent(t *testing.T) {
	const sentence = "synthetic-floor-sentence-must-not-leak"
	f, session, lease, in := receiptFixture(t)
	rules.New(f.db.App).Mount(f.mux)
	w := f.call(f.person, "POST", "/api/rules/layers", map[string]any{"layer": "company"}, "")
	expect(t, w, 200)
	layer := decode(t, w)["id"]
	w = f.call(f.person, "POST", "/api/rules/sets", map[string]any{"layer_id": layer, "name": "Safety"}, "")
	expect(t, w, 200)
	setID := decode(t, w)["id"].(string)
	rule := rules.Rule{Identity: "synthetic-floor", Text: sentence, Why: "The floor stays out of provenance.", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "AEON-219"}}
	expect(t, f.call(f.person, "PUT", "/api/rules/sets/"+setID+"/draft", map[string]any{"expected_revision": 1, "name": "Safety", "rules": []rules.Rule{rule}}, ""), 200)
	expect(t, f.call(f.person, "POST", "/api/rules/sets/"+setID+"/publish", map[string]any{"expected_revision": 2, "version": "260929120000.0.0"}, ""), 200)

	q := url.Values{"project_id": {f.project}, "person_id": {f.person.ID}, "agent_id": {f.agent.ID}, "role": {"builder"}, "harness": {"codex"}, "task_id": {f.ticket}}
	w = f.call(f.person, "GET", "/api/rules/merged?"+q.Encode(), nil, "")
	expect(t, w, 200)
	var merged rules.Merged
	if err := json.Unmarshal(w.Body.Bytes(), &merged); err != nil {
		t.Fatal(err)
	}
	if merged.SHA256 == "" || merged.Version != "260929120000.0.0" || len(merged.Versions) != 1 || merged.Versions[0].SetID != setID {
		t.Fatalf("merged sources %#v", merged.Versions)
	}
	in.BodySHA256 = merged.SHA256
	in.Version = merged.Version
	in.ByteSize = &merged.ByteSize
	path := session + "/rules-receipts"
	receiptResponse(t, f.call(f.agent, "POST", path, in, lease), false)
	receiptResponse(t, f.call(f.agent, "POST", path, in, lease), true)

	w = f.call(f.person, "GET", session+"/provenance", nil, "")
	expect(t, w, 200)
	body := w.Body.String()
	if strings.Contains(body, sentence) || strings.Contains(body, "The floor stays") || strings.Contains(body, lease) || strings.Contains(body, "/Users") {
		t.Fatal("provenance leaked rule text, a path, or the lease")
	}
	page := decode(t, w)
	revisions := page["revisions"].([]any)
	if len(revisions) != 1 {
		t.Fatalf("revisions %d", len(revisions))
	}
	items := revisions[0].(map[string]any)["items"].([]any)
	var sawMerged, sawSet bool
	for _, raw := range items {
		item := raw.(map[string]any)
		switch item["logical_name"] {
		case "merged-rules":
			sawMerged = item["kind"] == "rules_merged" && item["version"] == merged.Version && item["content_sha256"] == merged.SHA256
		case setID:
			sawSet = item["kind"] == "rules_set" && item["version"] == merged.Versions[0].Version && item["content_sha256"] == merged.Versions[0].SHA256 && item["byte_size"] == nil
		}
	}
	if !sawMerged || !sawSet {
		t.Fatalf("sources %s", body)
	}

	query := "/api/projects/" + f.project + "/instruction-provenance?version=" + url.QueryEscape(merged.Version) + "&kind=rules_set"
	w = f.call(f.person, "GET", query, nil, "")
	expect(t, w, 200)
	found := decode(t, w)
	hits := found["items"].([]any)
	if found["truncated"] != false || len(hits) != 1 || hits[0].(map[string]any)["session_id"] != strings.TrimPrefix(session, "/api/projects/"+f.project+"/harness-sessions/") || strings.Contains(w.Body.String(), sentence) {
		t.Fatalf("query %s", w.Body.String())
	}
	expect(t, f.call(f.person, "GET", "/api/projects/"+f.project+"/instruction-provenance?unexpected=1", nil, ""), 400)

	quiet := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "quiet-host",
		"harness_session_ref": "quiet-" + uid(), "worker_lease": "quiet-lease-0000000000000000000001",
		"management_mode": "unmanaged", "role": "worker",
	}, "")
	expect(t, quiet, 201)
	quietID := decode(t, quiet)["id"].(string)
	empty := decode(t, f.call(f.person, "GET", "/api/projects/"+f.project+"/harness-sessions/"+quietID+"/provenance", nil, ""))
	if len(empty["revisions"].([]any)) != 0 || empty["truncated"] != false {
		t.Fatal("session without sources", empty)
	}
	none := decode(t, f.call(f.person, "GET", "/api/projects/"+f.project+"/instruction-provenance?logical_name=AGENTS.md", nil, ""))
	if len(none["items"].([]any)) != 0 {
		t.Fatal("file sources appeared without files", none)
	}

	hidden := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','hidden')`, hidden.TenantID, hidden.ID)
		return err
	})
	expect(t, f.call(hidden, "GET", query, nil, ""), 404)
	expect(t, f.call(f.foreign, "GET", query, nil, ""), 404)

	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE harness_instruction_provenance_items SET version='260929120001.0.0' WHERE logical_name='merged-rules'`)
		return e
	})
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update err %v", err)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var payload string
		e := tx.QueryRow(t.Context(), `SELECT coalesce(string_agg(coalesce(before::text,'') || after::text, ''), '') FROM events WHERE type='harness.provenance_recorded'`).Scan(&payload)
		if e != nil {
			return e
		}
		if strings.Contains(payload, sentence) || strings.Contains(payload, lease) || !strings.Contains(payload, merged.SHA256) {
			t.Fatal("provenance audit leaked or dropped the digest")
		}
		var stored int
		e = tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_instruction_provenance_items WHERE logical_name='merged-rules' OR logical_name=$1`, setID).Scan(&stored)
		if e != nil {
			return e
		}
		if stored != 2 {
			t.Fatalf("stored sources %d", stored)
		}
		return nil
	})
}
