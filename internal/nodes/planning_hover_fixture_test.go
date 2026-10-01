// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Capture the real list response for both client test runners. Only session IDs
// are ignored when comparing planning: they are random, opaque identifiers.
var capturePlanningListFixture = flag.Bool("capture-planning-list-fixture", false, "emit the planning hover list fixture as base64")

func TestPlanningListHoverFixture(t *testing.T) {
	w := planningSetup(t)
	for _, tc := range []struct {
		key       string
		reported  bool
		input     int64
		estimated bool
	}{{"HOVER-1", false, 0, false}, {"HOVER-2", false, 0, false},
		{"HOVER-3", true, 1_000_000, false}, {"HOVER-4", true, 1_000_000, false},
		{"HOVER-5", true, 0, false}, {"HOVER-6", false, 0, true}} {
		fields := map[string]any{}
		if tc.estimated {
			fields["estimate_hours"] = .48
		}
		n := w.node(t, tc.key, "ticket", w.root.ID, "open", fields)
		if tc.reported {
			w.session(t, n.ID, "cursor", "grok-4.7", "xhigh", "grok-4.7", 60, tc.input, 0, 0, "api", "")
		}
		usageModel := ""
		if tc.key == "HOVER-3" {
			usageModel = "grok-4.7"
		}
		w.session(t, n.ID, "cursor", "grok-4.7", "xhigh", usageModel, 1, 100_000, 0, 0, "api", "")
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		// One live session per ticket except HOVER-2, which has stopped without
		// reporting usage. Older measured sessions remain stopped.
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET
            stopped_at=NULL,stop_reason=NULL,phase='working'
            WHERE id IN (SELECT DISTINCT ON (ticket_node_id) id FROM harness_sessions
                WHERE tenant_id=$1 AND ticket_node_id<>$2
                ORDER BY ticket_node_id,created_at DESC,id DESC)`, w.admin.TenantID, w.nodes["HOVER-2"].ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body := call(t, &w.admin, http.MethodGet, "/api/nodes?within="+w.root.ID+"&kind=ticket&sort=key", "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if len(page.Items) != 6 || page.Items[0].Planning.Tokens.Unreported != 1 ||
		page.Items[0].Planning.Cost == nil || !page.Items[0].Planning.Cost.ListUnpriced ||
		page.Items[2].Planning.Tokens.Sessions != 2 || page.Items[2].Planning.Tokens.Running != 1 {
		t.Fatalf("fixture did not exercise the server's unreported and running projections: %s", body)
	}
	if *capturePlanningListFixture {
		t.Logf("PLANNING_LIST_FIXTURE=%s", base64.StdEncoding.EncodeToString(body))
		return
	}
	want, err := os.ReadFile("../../web/tests/fixtures/planning-list.json")
	if err != nil {
		t.Fatal(err)
	}
	// Compare raw JSON subtrees so missing, null and extra server fields cannot
	// disappear through a hand-shaped client struct. The committed file retains
	// the complete, unmodified response, including its envelope and list rows.
	if !reflect.DeepEqual(planningFixtureProjection(t, body), planningFixtureProjection(t, want)) {
		t.Fatalf("server planning payload differs from the shared client fixture; recapture with -capture-planning-list-fixture: %s", body)
	}
}

func planningFixtureProjection(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	projection := map[string]any{}
	for _, item := range page.Items {
		planning := item["planning"].(map[string]any)
		for _, model := range planning["models"].([]any) {
			for _, session := range model.(map[string]any)["sessions"].([]any) {
				session.(map[string]any)["id"] = "session"
			}
		}
		projection[item["key"].(string)] = planning
	}
	return projection
}
