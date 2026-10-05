// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestPlanningRegistryVersionAndEffortPins(t *testing.T) {
	w := planningSetup(t)
	n := w.node(t, "PIN-1", "work", w.root.ID, "open", nil)
	w.session(t, n.ID, "claude", "legacy-5", "high", "legacy-5", 1, 200, 0, 0, "unknown", "")
	w.session(t, n.ID, "claude", "legacy-5-5", "xhigh", "legacy-5-5", 1, 100, 0, 0, "unknown", "")
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		for _, version := range []string{"5", "5.5"} {
			raw, _ := json.Marshal(map[string]string{"display_name": "Claude Opus", "short_name": "Opus", "model_version": version})
			var profile string
			if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,display_overrides) VALUES($1,$2,'profile-revision','claude','anthropic','opus','high','strong',$3) RETURNING id::text`, w.admin.TenantID, "pin-"+strings.ReplaceAll(version, ".", "-"), string(raw)).Scan(&profile); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET model_profile_id=$2::uuid WHERE ticket_node_id=$1 AND model=$3`, n.ID, profile, "legacy-"+strings.ReplaceAll(version, ".", "-"))
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&sort=model")[n.Key]
	if got == nil || len(got.Models) != 2 {
		t.Fatalf("distinct version pins collapsed: %+v", got)
	}
	if got.Models[0].FullName() != "Claude Opus 5" || got.Models[1].FullName() != "Claude Opus 5.5" {
		t.Fatalf("full version identities: %+v", got.Models)
	}
	if level := got.Models[0].Sessions[0].EffortLevel; level == nil || *level != 3 {
		t.Fatal("matching registered effort missing")
	}
	if got.Models[1].Sessions[0].EffortLevel != nil {
		t.Fatal("mismatched profile effort was guessed")
	}
}

func TestPlanningActualModels(t *testing.T) {
	w := planningSetup(t)
	ticket := w.node(t, "USED-1", "work", w.root.ID, "open", map[string]any{"route_role": "build", "area": "backend"})
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
	path := "/api/nodes?within=" + w.root.ID + "&kind=work&sort=key"
	for _, who := range []struct {
		name  string
		admin bool
	}{{"admin", true}, {"no-cost", false}} {
		principal := w.admin
		if !who.admin {
			principal = w.viewer
		}
		got := planningOf(t, principal, path)[ticket.Key]
		// Cancellation removes a leaf from estimates, not its historical spend.
		if got == nil || len(got.Models) != 3 || got.Tokens.Running != 1 || got.Tokens.Spent == nil || *got.Tokens.Spent != 3150+500+9000 {
			t.Fatalf("%s rollup: %+v", who.name, got)
		}
		byModel := map[string]planningModel{}
		for _, model := range got.Models {
			byModel[model.Model] = model
		}
		main := byModel["gpt-6-sol"]
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
		child := byModel["opus"]
		if len(child.Sessions) != 1 || !child.Sessions[0].Running {
			t.Fatal("child session running flag missing")
		}
		closed := byModel["excluded"]
		if len(closed.Sessions) != 1 || closed.Sessions[0].Running || planningModelTokens(closed) != 9000 {
			t.Fatal("cancelled leaf lost its historical model usage", closed)
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
	if got == nil || len(got.Models) != 2 || got.Tokens.Running != 0 || got.Tokens.Spent == nil || *got.Tokens.Spent != 3150+9000 {
		t.Fatalf("source visibility: %+v", got)
	}
	for _, model := range got.Models {
		if model.Model != "gpt-6-sol" && model.Model != "excluded" {
			t.Fatalf("hidden descendant model leaked: %+v", model)
		}
	}
}

func TestPlanningRawAndUnknownSessionModels(t *testing.T) {
	w := planningSetup(t)
	n := w.node(t, "RAW-1", "work", w.root.ID, "open", nil)
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
	// Preserve a real model-bearing owner fixture during the foreign read.
	foreign := addPrincipal(t, "foreign-models")
	got = planningOf(t, w.admin, path)[n.Key]
	if got == nil || len(got.Models) != 1 || got.Models[0].Model != "unregistered-model" {
		t.Fatalf("owner model fixture lost before isolation check: %+v", got)
	}
	for key, p := range planningOf(t, foreign, "/api/nodes") {
		if key == n.Key || p != nil && len(p.Models) > 0 {
			t.Fatalf("foreign tenant leaked owner model projection: %s %+v", key, p)
		}
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
}
