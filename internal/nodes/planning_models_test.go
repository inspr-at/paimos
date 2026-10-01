// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestPlanningActualModels(t *testing.T) {
	w := planningSetup(t)
	ticket := w.node(t, "USED-1", "ticket", w.root.ID, "open", map[string]any{"route_role": "build", "area": "backend"})
	task := w.node(t, "USED-2", "task", ticket.ID, "open", nil)
	cancelled := w.node(t, "USED-3", "task", ticket.ID, "cancelled", nil)
	w.session(t, ticket.ID, "codex", "legacy-model", "xhigh", "gpt-6-sol", 60, 1000, 50, 100, "api", "")
	w.session(t, ticket.ID, "codex", "gpt-6-sol", "high", "gpt-6-sol", 60, 2000, 0, 0, "subscription", "Pro")
	w.session(t, task.ID, "claude", "opus", "high", "opus", 30, 500, 0, 0, "unknown", "")
	w.session(t, cancelled.ID, "cursor", "excluded", "xhigh", "excluded", 30, 9000, 0, 0, "unknown", "")
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET model_profile_id=(SELECT id FROM model_profiles WHERE slug='codex-sol-xhigh'),model_raw='gpt-6-sol-xhigh' WHERE ticket_node_id=$1 AND model='legacy-model'`, ticket.ID)
		if err != nil {
			return err
		}
		// A second usage row must not duplicate the model's session count.
		_, err = tx.Exec(t.Context(), `INSERT INTO harness_session_usage(tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,billing_mode)
            SELECT tenant_id,id,'other-usage-model',1,100,0,0,false,'unknown' FROM harness_sessions WHERE ticket_node_id=$1 AND model='legacy-model'`, ticket.ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=NULL,stop_reason=NULL,phase='working' WHERE ticket_node_id=$1`, task.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/nodes?within=" + w.root.ID + "&kind=ticket&sort=key"
	for _, who := range []struct {
		name  string
		admin bool
	}{{"admin", true}, {"no-cost", false}} {
		principal := w.admin
		if !who.admin {
			principal = w.viewer
		}
		got := planningOf(t, principal, path)[ticket.Key]
		if got == nil || len(got.Models) != 2 || got.Tokens.Running != 1 {
			t.Fatalf("%s rollup: %+v", who.name, got)
		}
		main := got.Models[0]
		if main.Model != "gpt-6-sol" || main.Label != "Codex sol" || len(main.Sessions) != 2 || planningModelTokens(main) != 3150 {
			t.Fatalf("model/session rollup: %+v", main)
		}
		foundRaw := false
		for _, s := range main.Sessions {
			if s.Raw != nil && *s.Raw == "gpt-6-sol-xhigh" {
				foundRaw = s.ProfileID != nil && s.Tokens != nil && *s.Tokens == 1150 && s.EffortLevel != nil && *s.EffortLevel == 4
			}
		}
		if main.DisplayName != "Codex Sol" || main.ModelVersion != "6" {
			t.Fatalf("registry display identity: %+v", main)
		}
		for _, s := range main.Sessions {
			if s.ProfileID == nil && s.EffortLevel != nil {
				t.Fatal("unregistered session effort was guessed")
			}
		}
		if !foundRaw {
			t.Fatalf("profile precedence/audit missing: %+v", main)
		}
		if !got.Models[1].Sessions[0].Running {
			t.Fatal("child session running flag missing")
		}
		if !who.admin && got.Cost != nil {
			t.Fatal("model rollup leaked costs")
		}
	}
	// Moving a descendant session to a project the caller cannot see excludes it.
	other := mustNode(t, w.admin, `{"kind_id":"`+kindBySlug(t, w.admin, "project").ID+`","title":"Hidden"}`)
	scoped := insertPerson(t, w.admin.TenantID, "Project viewer")
	bindProjectRole(t, w.admin.TenantID, scoped.ID, "viewer", w.root.ID)
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET project_id=$2 WHERE ticket_node_id=$1`, task.ID, other.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := planningOf(t, scoped, path)[ticket.Key]
	// The caller can read the ticket but cannot see the source session project.
	if got == nil || len(got.Models) != 1 || got.Models[0].Model != "gpt-6-sol" {
		t.Fatalf("source visibility: %+v", got)
	}
}

func TestPlanningRawAndUnknownSessionModels(t *testing.T) {
	w := planningSetup(t)
	n := w.node(t, "RAW-1", "ticket", w.root.ID, "open", nil)
	w.session(t, n.ID, "cursor", "", "", "", 1, 0, 0, 0, "unknown", "")
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET model_raw='unregistered-model',stopped_at=NULL,stop_reason=NULL,phase='working' WHERE ticket_node_id=$1`, n.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/nodes?within=" + w.root.ID
	got := planningOf(t, w.admin, path)[n.Key]
	if got == nil || len(got.Models) != 1 || got.Models[0].Model != "unregistered-model" || got.Models[0].Sessions[0].Tokens != nil || got.Tokens.Running != 1 {
		t.Fatalf("raw fallback: %+v", got)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET model_raw=NULL WHERE ticket_node_id=$1`, n.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got = planningOf(t, w.admin, path)[n.Key]
	if got == nil || len(got.Models) != 0 || got.Tokens.Running != 1 {
		t.Fatalf("unknown model must retain running count: %+v", got)
	}
	foreign := newPrincipal(t, "foreign-models")
	for key, p := range planningOf(t, foreign, "/api/nodes") {
		if p != nil && len(p.Models) > 0 {
			t.Fatalf("foreign tenant %s leaked models: %s", key, strings.TrimSpace(p.Models[0].Model))
		}
	}
}
