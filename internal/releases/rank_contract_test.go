// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

func rankReceiptSchema(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	response := spec["paths"].(map[string]any)["/projects/{projectId}/releases/{releaseId}/rank"].(map[string]any)["post"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)
	ref := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
	if ref != "#/components/schemas/DeliveryReleaseRow" {
		t.Fatalf("unexpected rank response schema: %v", ref)
	}
	schema := spec["components"].(map[string]any)["schemas"].(map[string]any)["DeliveryReleaseRow"].(map[string]any)["properties"].(map[string]any)["undo_event_id"]
	receipt, ok := schema.(map[string]any)
	if !ok {
		t.Fatal("rank response contract omits undo_event_id")
	}
	types, ok := receipt["type"].([]any)
	if !ok || len(types) != 2 || !slices.Contains(types, any("integer")) || !slices.Contains(types, any("null")) || receipt["minimum"] != 1 {
		t.Fatalf("receipt must be a positive nullable integer: %+v", receipt)
	}
	return receipt
}

func TestRankResponseContractDeclaresNullableReceipt(t *testing.T) { rankReceiptSchema(t) }

func TestRankHTTPReceiptConformsToResponseContract(t *testing.T) {
	rankReceiptSchema(t)
	f := adoptFixture(t)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "owner")
	second := f.existing("release", f.project, "Internal release", "open")
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,visibility,rank) VALUES($1,$2,$3,'internal','W')`, f.person.TenantID, f.project, second)
		return err
	})
	base := "/api/projects/" + f.project + "/releases/" + second
	w := f.request(f.person, "POST", base+"/rank", fmt.Sprintf(`{"expected_revision":1,"before_id":%q}`, f.release))
	if w.Code != 200 {
		t.Fatalf("rank status %d: %s", w.Code, w.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var receipt int64
	if err := json.Unmarshal(body["undo_event_id"], &receipt); err != nil || receipt < 1 {
		t.Fatalf("rank receipt violates schema: %s (%v)", body["undo_event_id"], err)
	}
	var eventID int64
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE node_id=$1 AND type='release.reranked' ORDER BY id DESC LIMIT 1`, second).Scan(&eventID)
	})
	if receipt != eventID {
		t.Fatalf("receipt %d does not name committed rank event %d", receipt, eventID)
	}
	w = f.request(f.person, "GET", base, "")
	if w.Code != 200 {
		t.Fatalf("read status %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["undo_event_id"]) != "null" {
		t.Fatalf("read must return nullable receipt: %s", body["undo_event_id"])
	}
}
