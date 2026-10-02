// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"encoding/json"
	"fmt"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
)

func TestReviewStoresLoosenedResidencyLockTrace(t *testing.T) {
	f := newFixture(t)
	project := testID()
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title) SELECT $1,$2,id,'TRACE-1','Trace project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticket, project); err != nil {
			return err
		}
		eu, any := "eu", "any"
		if _, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "person", PersonID: &f.person.ID, Residency: &eu, ResidencyLocked: true}); err != nil {
			return err
		}
		_, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "project", ProjectID: &project, Residency: &any})
		return err
	})
	var created Review
	in := f.input()
	f.call(t, f.person, "POST", "/api/nodes/"+f.ticket+"/reviews", in, 201, &created)
	if created.RunID == nil {
		t.Fatal("loosened project did not queue the qualified reviewer")
	}
	var reviews []Review
	f.call(t, f.person, "GET", "/api/nodes/"+f.ticket+"/reviews", nil, 200, &reviews)
	if len(reviews) != 1 {
		t.Fatal("missing stored review")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var runTrace json.RawMessage
		if err := tx.QueryRow(t.Context(), `SELECT trace FROM agent_runs WHERE id=$1`, *created.RunID).Scan(&runTrace); err != nil {
			return err
		}
		for _, raw := range []json.RawMessage{created.Trace, reviews[0].Trace, runTrace} {
			var trace modelregistry.PreferenceTrace
			if err := json.Unmarshal(raw, &trace); err != nil {
				return err
			}
			if trace.Residency.Value != "any" || !trace.Residency.LoosenedLock || len(trace.Residency.LoosenedLocks) != 1 || trace.Residency.LoosenedLocks[0] != (modelprefs.ResidencyLock{Level: "person", Value: "eu"}) || trace.PersonID == nil || *trace.PersonID != f.person.ID {
				t.Fatalf("stored review/run lost loosening evidence: %+v", trace)
			}
		}
		return nil
	})
}

func TestReviewPreferencesStayWithinQualifiedSet(t *testing.T) {
	for _, tc := range []struct {
		name, c, role, pin    string
		wantComplex, security bool
	}{
		{name: "normal", c: "M", role: "build"}, {name: "large", c: "L", role: "build", wantComplex: true},
		{name: "latest qualified", c: "L", role: "build", pin: "latest", wantComplex: true},
		{name: "role fallback", role: "build-hard", wantComplex: true}, {name: "explicit small", c: "S", role: "build-hard"},
		{name: "codex pin rejected", c: "L", role: "build", pin: "codex"},
		{name: "author family rejected", c: "L", role: "build", pin: "author"},
		{name: "off ladder rejected", c: "L", role: "build", pin: "off-ladder"},
		{name: "decoded security", c: "L", role: "build", security: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			complexProfile := testID()
			f.tx(t, func(tx pgx.Tx) error {
				if err := modelprefs.SeedKinds(t.Context(), tx, f.person.TenantID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier)
    VALUES($1,$2,'complex-review','1','claude','anthropic','opus','xhigh','strong')`, f.person.TenantID, complexProfile); err != nil {
					return err
				}
				if tc.pin != "off-ladder" {
					if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'review-gate',4,$2)`, f.person.TenantID, complexProfile); err != nil {
						return err
					}
				}
				pin := complexProfile
				if tc.pin == "codex" || tc.pin == "author" {
					if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE family='openai'`).Scan(&pin); err != nil {
						return err
					}
				}
				scope, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "person", PersonID: &f.person.ID})
				if err != nil {
					return err
				}
				var kind string
				if err := tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='review'`).Scan(&kind); err != nil {
					return err
				}
				complexCell := modelprefs.Cell{Mode: "pinned", ProfileID: pin}
				if tc.pin == "latest" {
					complexCell = modelprefs.Cell{Mode: "latest", Family: "anthropic", Line: "opus", Effort: "xhigh"}
				}
				if err := modelprefs.PutRow(t.Context(), tx, f.person, scope, kind, modelprefs.Row{Cells: map[string]modelprefs.Cell{
					"normal": {Mode: "pinned", ProfileID: f.profile}, "complex": complexCell}}); err != nil {
					return err
				}
				area := "backend"
				if tc.security {
					area = " security "
				}
				_, err = tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||jsonb_build_object('area',$2::text,'complexity',$3::text,'route_role',$4::text) WHERE id=$1`, f.ticket, area, tc.c, tc.role)
				return err
			})
			in := f.input()
			if tc.pin == "codex" {
				in.AuthorFamily = "xai"
			}
			var review Review
			f.call(t, f.person, "POST", "/api/nodes/"+f.ticket+"/reviews", in, 201, &review)
			if tc.security {
				if review.RunID != nil || review.Status != "blocked" {
					t.Fatal("security used generic ladder", review)
				}
				return
			}
			want := f.profile
			if tc.wantComplex {
				want = complexProfile
			}
			if review.ProfileID == nil || *review.ProfileID != want {
				t.Fatal("wrong qualified reviewer", tc, review)
			}
			f.tx(t, func(tx pgx.Tx) error {
				var starter *string
				var residency *string
				if err := tx.QueryRow(t.Context(), `SELECT prefs_person_id::text,residency FROM agent_runs WHERE id=$1`, *review.RunID).Scan(&starter, &residency); err != nil {
					return err
				}
				if starter == nil || *starter != f.person.ID || residency != nil {
					t.Fatal("review stamp drifted", starter, residency)
				}
				return nil
			})
		})
	}
}

func TestReviewStarterPersonAndOperatorKeyCompatibility(t *testing.T) {
	for _, kind := range []string{"person", "linked-person", "creator-key", "operator-key"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			requester := f.person
			want := f.person.ID
			if strings.Contains(kind, "key") {
				requester = f.agent
				if kind == "creator-key" {
					requester.KeyCreatorID = f.person.ID
				} else {
					want = ""
				}
			} else if kind == "linked-person" {
				alias := testID()
				f.tx(t, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name,linked_to) VALUES($1,$2,'person','Linked starter',$3)`, f.person.TenantID, alias, f.person.ID)
					return err
				})
				requester = tenant.Principal{ID: alias, TenantID: f.person.TenantID, Kind: tenant.Person}
			}
			in := f.input()
			if requester.Kind == tenant.Agent {
				var order workorders.Order
				f.call(t, f.agent, "POST", "/api/work-orders", map[string]any{"title": "Author", "parent_id": f.ticket, "assignee_principal_id": f.agent.ID, "criteria": []string{"Pass"}}, 201, &order)
				var source string
				f.tx(t, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,status)
    SELECT $1,$2,$3,p.id,p.model,'completed' FROM model_profiles p WHERE p.family='openai' RETURNING id::text`, f.person.TenantID, order.NodeID, f.agent.ID).Scan(&source)
				})
				in.AuthorRunID = &source
			}
			var review Review
			f.call(t, requester, "POST", "/api/nodes/"+f.ticket+"/reviews", in, 201, &review)
			if review.RunID == nil || review.ProfileID == nil || *review.ProfileID != f.profile || review.Status != "queued" {
				t.Fatal("empty matrix changed request", review)
			}
			f.tx(t, func(tx pgx.Tx) error {
				var starter *string
				if err := tx.QueryRow(t.Context(), `SELECT prefs_person_id::text FROM agent_runs WHERE id=$1`, *review.RunID).Scan(&starter); err != nil {
					return err
				}
				if want == "" && starter != nil || want != "" && (starter == nil || *starter != want) {
					return fmt.Errorf("wrong starter for %s", kind)
				}
				return nil
			})
		})
	}
}
