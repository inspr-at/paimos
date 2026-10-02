// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"fmt"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
)

func TestReviewPreferencesStayWithinQualifiedSet(t *testing.T) {
	for _, tc := range []struct {
		name, c, role, pin    string
		wantComplex, security bool
	}{
		{name: "normal", c: "M", role: "build"}, {name: "large", c: "L", role: "build", wantComplex: true},
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
				if err := modelprefs.PutRow(t.Context(), tx, f.person, scope, kind, modelprefs.Row{Cells: map[string]modelprefs.Cell{
					"normal": {Mode: "pinned", ProfileID: f.profile}, "complex": {Mode: "pinned", ProfileID: pin}}}); err != nil {
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
