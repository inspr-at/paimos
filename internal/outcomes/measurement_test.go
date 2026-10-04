// SPDX-License-Identifier: AGPL-3.0-only
package outcomes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestTicketMeasurementEvidenceKinds(t *testing.T) {
	d := dbtest.Open(t)
	p := newPerson(t, d, "measurement-kinds")
	project := insertNode(t, d, p, "project", "MEA-1", "Measurement", nil)
	ticket := insertNode(t, d, p, "ticket", "MEA-2", "Evidence", &project)
	mod := New(d.App)
	read := func() ticketMeasurement {
		t.Helper()
		r := callAs(t, mod, p, http.MethodGet, "/api/outcomes/measurement?ticket_node_id="+ticket, "")
		if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("measurement: %d %s", r.Code, r.Body.String())
		}
		var got ticketMeasurement
		if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	t.Run("completion", func(t *testing.T) {
		r := callAs(t, nodes.New(d.App, nil), p, "PATCH", "/api/nodes/"+ticket, `{"state":"done","fields":`+benefitFields+`}`)
		if r.Code != 200 {
			t.Fatalf("completion: %d %s", r.Code, r.Body.String())
		}
		got := read()
		if got.Completions != 1 || got.Reviews != 0 || got.Fixes != 0 {
			t.Fatal(got)
		}
	})
	for _, tc := range []struct {
		name, kind, payload string
		reviews, fixes      int
	}{
		{"review", "review_verdict", `{"verdict":"changes","round":1}`, 1, 0},
		{"fix", "fix_round", `{"round":1}`, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"kind":"` + tc.kind + `","ticket":"` + ticket + `","payload":` + tc.payload + `}`
			// Exercise the authenticated recording route, including its idempotency.
			mux := http.NewServeMux()
			mod.Mount(mux)
			req := httptest.NewRequest("POST", "/api/outcomes", strings.NewReader(body))
			req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
			req.Header.Set("Idempotency-Key", "measurement-"+tc.name)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != 201 {
				t.Fatalf("record: %d %s", rec.Code, rec.Body.String())
			}
			got := read()
			if got.Reviews != tc.reviews || got.Fixes != tc.fixes || got.Completions != 1 {
				t.Fatal(got)
			}
		})
	}
	t.Run("diff", func(t *testing.T) {
		inTenant(t, d, p, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,ticket_node_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,commits)
 VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker',decode(md5('ref'),'hex'),decode(md5('lease'),'hex'),$5::jsonb),
 ($1,$2,$3,$4,'codex','test','unmanaged','worker',decode(md5('ref2'),'hex'),decode(md5('lease2'),'hex'),$5::jsonb)`, p.TenantID, project, ticket, p.ID, `[{"sha":"abcdef0","subject":"Measured","lines_added":17,"lines_deleted":3,"files_changed":2}]`)
			return err
		})
		got := read()
		if got.Commits != 1 || got.Added == nil || *got.Added != 17 || *got.Deleted != 3 || *got.Files != 2 {
			t.Fatal(got)
		}
		inTenant(t, d, p, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET commits=commits||'[{"sha":"abcdef1","subject":"Unknown"}]'::jsonb WHERE ticket_node_id=$1`, ticket)
			return err
		})
		got = read()
		if got.Commits != 2 || got.Added != nil || got.Deleted != nil || got.Files != nil {
			t.Fatal("missing diff became zero", got)
		}
	})
	foreign := newPerson(t, d, "measurement-foreign")
	if r := callAs(t, mod, foreign, "GET", "/api/outcomes/measurement?ticket_node_id="+ticket, ""); r.Code != 404 {
		t.Fatalf("foreign: %d %s", r.Code, r.Body.String())
	}
	if r := callAs(t, mod, p, "GET", "/api/outcomes/measurement", ""); r.Code != 400 {
		t.Fatalf("missing ticket: %d", r.Code)
	}
}
