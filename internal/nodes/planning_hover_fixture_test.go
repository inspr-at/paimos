// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Capture the real list response for both client test runners. Session IDs and
// their ID-based ordering vary between runs; compare the complete session set.
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
		{"HOVER-5", true, 0, false}, {"HOVER-6", false, 0, true},
		{"HOVER-7", true, 1_000_000, false}, {"HOVER-8", true, 1_000_000, false}} {
		fields := map[string]any{}
		if tc.estimated {
			fields["estimate_hours"] = .48
		}
		n := w.node(t, tc.key, "work", w.root.ID, "open", fields)
		if tc.reported {
			billing, plan := "api", ""
			if tc.key == "HOVER-8" {
				billing, plan = "subscription", "Pro"
			}
			w.session(t, n.ID, "cursor", "grok-4.7", "xhigh", "grok-4.7", 60, tc.input, 0, 0, billing, plan)
		}
		usageModel, billing := "", "api"
		if tc.key == "HOVER-3" {
			usageModel = "grok-4.7"
		} else if tc.key == "HOVER-7" {
			// Real reported usage without either a list price or known billing.
			usageModel, billing = "unpriced-model", "unknown"
		}
		w.session(t, n.ID, "cursor", "grok-4.7", "xhigh", usageModel, 1, 100_000, 0, 0, billing, "")
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		// One live session per ticket except the usage-less stopped HOVER-2
		// and finished subscription HOVER-8. Older measured sessions stay stopped.
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET
            stopped_at=NULL,stop_reason=NULL,phase='working'
            WHERE id IN (SELECT DISTINCT ON (ticket_node_id) id FROM harness_sessions
                WHERE tenant_id=$1 AND ticket_node_id<>$2 AND ticket_node_id<>$3
                ORDER BY ticket_node_id,created_at DESC,id DESC)`, w.admin.TenantID, w.nodes["HOVER-2"].ID, w.nodes["HOVER-8"].ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body := call(t, &w.admin, http.MethodGet, "/api/nodes?within="+w.root.ID+"&kind=work&sort=key", "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if len(page.Items) != 8 {
		t.Fatalf("fixture row count: %d", len(page.Items))
	}
	for _, item := range page.Items {
		p := item.Planning
		if p == nil || p.Cost == nil {
			t.Fatalf("%s missing planning or cost", item.Key)
		}
		if item.Key == "HOVER-7" {
			if p.Tokens.Unreported != 0 || !p.Cost.ListUnpriced || !p.Cost.PaidUnknown ||
				!slices.Equal(p.Cost.BillingModes, []string{"api", "unknown"}) ||
				p.Cost.ListSpent == nil || *p.Cost.ListSpent != "2.000000" {
				t.Fatalf("real unpriced usage lost its warnings: %+v", p)
			}
			continue
		}
		// HOVER-6 has a token estimate but no priced/billed route. Estimate
		// warnings remain independent of the session's absent usage row.
		unpricedEstimate := item.Key == "HOVER-6"
		if p.Cost.ListUnpriced != unpricedEstimate || p.Cost.PaidUnknown != unpricedEstimate {
			t.Fatalf("%s session without usage changed billing flags: %+v", item.Key, p.Cost)
		}
		if item.Key == "HOVER-3" {
			if p.Tokens.Sessions != 2 || p.Tokens.Running != 1 || p.Tokens.Unreported != 0 {
				t.Fatalf("measured fixture live/total counts: %+v", p.Tokens)
			}
		} else if p.Tokens.Unreported != 1 {
			t.Fatalf("%s should retain one unreported session: %+v", item.Key, p.Tokens)
		}
		switch item.Key {
		case "HOVER-1", "HOVER-2", "HOVER-6":
			if p.Cost.ListSpent != nil || len(p.Cost.BillingModes) != 0 {
				t.Fatalf("%s usage-less fixture invented billing: %+v", item.Key, p.Cost)
			}
		case "HOVER-3", "HOVER-4", "HOVER-5":
			if !slices.Equal(p.Cost.BillingModes, []string{"api"}) {
				t.Fatalf("%s API usage billing: %+v", item.Key, p.Cost)
			}
		case "HOVER-8":
			if !slices.Equal(p.Cost.BillingModes, []string{"subscription"}) || !slices.Equal(p.Cost.Plans, []string{"Pro"}) {
				t.Fatalf("unreported session changed subscription billing: %+v", p.Cost)
			}
		}
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
		models, _ := planning["models"].([]any) // Absent on a ticket with no sessions; absence stays in the raw comparison.
		for _, model := range models {
			sessions := model.(map[string]any)["sessions"].([]any)
			for _, session := range sessions {
				session.(map[string]any)["id"] = "session"
			}
			sort.Slice(sessions, func(i, j int) bool {
				a, _ := json.Marshal(sessions[i])
				b, _ := json.Marshal(sessions[j])
				return string(a) < string(b)
			})
		}
		projection[item["key"].(string)] = planning
	}
	return projection
}
