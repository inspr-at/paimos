// SPDX-License-Identifier: AGPL-3.0-only
package outcomes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestTicketMeasurementProjectScopeThroughMiddleware(t *testing.T) {
	d := dbtest.Open(t)
	owner := newPerson(t, d, "measurement-middleware")
	projectA := insertNode(t, d, owner, "project", "MMA-1", "Allowed", nil)
	projectB := insertNode(t, d, owner, "project", "MMB-1", "Forbidden", nil)
	ticketA := insertNode(t, d, owner, "ticket", "MMA-2", "Allowed evidence", &projectA)
	ticketB := insertNode(t, d, owner, "ticket", "MMB-2", "Forbidden evidence", &projectB)
	reader, token := agentKey(t, d, owner.TenantID, "measurement-project-reader", []string{"nodes.read", "outcome.read", "harness.read"})
	foreign := newPerson(t, d, "measurement-middleware-foreign")
	_, foreignToken := agentKey(t, d, foreign.TenantID, "measurement-foreign-reader", []string{"nodes.read", "outcome.read", "harness.read"})
	var identityID, personID string
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject) VALUES('measurement-test','project-reader') RETURNING id::text`).Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte{9}, 32)
	digest := sha256.Sum256(raw)
	cookie := hex.EncodeToString(raw)
	inTenant(t, d, owner, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,identity_id,kind,name) VALUES($1,$2,'person','Project reader') RETURNING id::text`, owner.TenantID, identityID).Scan(&personID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO sessions(id,identity_id,tenant_id,principal_id,expires_at) VALUES($1,$2,$3,$4,now()+interval '1 day')`, hex.EncodeToString(digest[:]), identityID, owner.TenantID, personID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, owner.TenantID, reader); err != nil {
			return err
		}
		var nodeRole string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'measurement_nodes_only','Nodes only') RETURNING id::text`, owner.TenantID).Scan(&nodeRole); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read')`, owner.TenantID, nodeRole); err != nil {
			return err
		}
		for _, principalID := range []string{reader, personID} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
 SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, owner.TenantID, principalID, projectA); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, owner.TenantID, principalID, nodeRole, projectB); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO outcome_events(tenant_id,kind,project_id,ticket_node_id,idempotency_key,actor_principal_id,source,payload,request_digest)
 VALUES($1,'review_verdict',$2,$3,'middleware-review-a',$6,'recorded','{"verdict":"ok"}',decode(md5('review-a'),'hex')),
 ($1,'review_verdict',$4,$5,'middleware-review-b',$6,'recorded','{"verdict":"changes"}',decode(md5('review-b'),'hex'))`, owner.TenantID, projectA, ticketA, projectB, ticketB, owner.ID)
		return err
	})
	mod, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{7}, 32)}, d.App)
	if err != nil {
		t.Fatal(err)
	}
	api := outcomesAPI{t: t, auth: mod, mod: New(d.App)}
	personCall := func(ref string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		api.mod.Mount(mux)
		req := httptest.NewRequest(http.MethodGet, "/api/outcomes/measurement?ticket_node_id="+ref, nil)
		_, req.Pattern = mux.Handler(req)
		req.AddCookie(&http.Cookie{Name: "aeon_session", Value: cookie})
		res := httptest.NewRecorder()
		mod.Middleware(mux).ServeHTTP(res, req)
		return res
	}
	for _, tc := range []struct {
		name, ref, credential string
		status                int
	}{
		{"allowed id", ticketA, token, http.StatusOK},
		{"allowed key", "MMA-2", token, http.StatusOK},
		// B stays visible through nodes.read: denial must come from the
		// target's evidence permissions, not merely from a missing row.
		{"forbidden id", ticketB, token, http.StatusForbidden},
		{"forbidden key", "MMB-2", token, http.StatusForbidden},
		{"person allowed id", ticketA, "person", http.StatusOK},
		{"person allowed key", "MMA-2", "person", http.StatusOK},
		{"person forbidden id", ticketB, "person", http.StatusForbidden},
		{"person forbidden key", "MMB-2", "person", http.StatusForbidden},
		{"foreign tenant", ticketA, foreignToken, http.StatusNotFound},
		{"anonymous", ticketA, "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got *httptest.ResponseRecorder
			if tc.credential == "person" {
				got = personCall(tc.ref)
			} else {
				got = api.call(tc.credential, http.MethodGet, "/api/outcomes/measurement?ticket_node_id="+tc.ref, "", "")
			}
			if got.Code != tc.status {
				t.Fatalf("measurement: %d %s, want %d", got.Code, got.Body.String(), tc.status)
			}
			if tc.status == http.StatusForbidden {
				var denial struct{ Error string }
				if err := json.Unmarshal(got.Body.Bytes(), &denial); err != nil || denial.Error != "permission denied" {
					t.Fatalf("wrong denial reason: %s (%v)", got.Body.String(), err)
				}
			}
			if tc.status != http.StatusOK {
				return
			}
			var view ticketMeasurement
			if err := json.Unmarshal(got.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if view.TicketID != ticketA || view.Reviews != 1 || view.Completions != 0 || view.Fixes != 0 || got.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("allowed evidence: %+v", view)
			}
			if view.Added != nil || view.Deleted != nil || view.Files != nil || !slices.Contains(view.Gaps, "diff_visibility_incomplete") {
				t.Fatalf("project reader certified a diff subset: %+v", view)
			}
		})
	}
	// Keep both fixtures: hiding B from the reader must not remove its evidence.
	if countOutcomes(t, d, owner, ticketA, "review_verdict") != 1 || countOutcomes(t, d, owner, ticketB, "review_verdict") != 1 {
		t.Fatal("project evidence fixtures were not retained")
	}
}

func TestTicketMeasurementCanonicalCommitIdentity(t *testing.T) {
	d := dbtest.Open(t)
	p := newPerson(t, d, "measurement-identity")
	project := insertNode(t, d, p, "project", "MID-1", "Identity", nil)
	ticket := insertNode(t, d, p, "ticket", "MID-2", "Diff identity", &project)
	inTenant(t, d, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,ticket_node_id,agent_principal_id,harness,host,management,role,work_shape,ref_digest,lease_digest)
 VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',decode(md5('identity-ref'),'hex'),decode(md5('identity-lease'),'hex')),
 ($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',decode(md5('identity-ref-2'),'hex'),decode(md5('identity-lease-2'),'hex'))`, p.TenantID, project, ticket, p.ID)
		return err
	})
	const full = "abcdef0123456789012345678901234567890123"
	const samePrefix = "abcdef0123456789012345678901234567890124"
	commit := func(sha string, added int64) diffCommit {
		deleted, files := int64(3), int64(2)
		return diffCommit{SHA: sha, Added: &added, Deleted: &deleted, Files: &files}
	}
	for _, tc := range []struct {
		name        string
		commits     []diffCommit
		count       int
		complete    bool
		identityGap bool
	}{
		{"full duplicates", []diffCommit{commit(full, 17), commit(full, 17)}, 1, true, false},
		{"case duplicates", []diffCommit{commit(full, 17), commit(strings.ToUpper(full), 17)}, 1, true, false},
		{"short then full", []diffCommit{commit(full[:7], 17), commit(full, 17)}, 1, false, true},
		{"full then short", []diffCommit{commit(full, 17), commit(full[:7], 17)}, 1, false, true},
		{"39 character alias", []diffCommit{commit(full[:39], 17), commit(full, 17)}, 1, false, true},
		{"short only", []diffCommit{commit(full[:7], 17)}, 0, false, true},
		{"invalid full identity", []diffCommit{commit("g"+full[1:], 17), commit(full, 17)}, 1, false, true},
		{"distinct full same prefix", []diffCommit{commit(full, 17), commit(samePrefix, 17)}, 2, true, false},
		{"conflicting full readings", []diffCommit{commit(full, 17), commit(full, 18)}, 1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each array is retained on two sessions, exercising deduplication
			// both within one report and across contributors' reports.
			raw, err := json.Marshal(tc.commits)
			if err != nil {
				t.Fatal(err)
			}
			inTenant(t, d, p, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET commits=$2::jsonb WHERE ticket_node_id=$1`, ticket, string(raw))
				return err
			})
			r := callAs(t, New(d.App), p, http.MethodGet, "/api/outcomes/measurement?ticket_node_id="+ticket, "")
			if r.Code != http.StatusOK {
				t.Fatalf("measurement: %d %s", r.Code, r.Body.String())
			}
			var got ticketMeasurement
			if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Commits != tc.count || slices.Contains(got.Gaps, "commit_identity_incomplete") != tc.identityGap || got.Truncated {
				t.Fatalf("commit identity: %+v", got)
			}
			if !tc.complete {
				if got.Added != nil || got.Deleted != nil || got.Files != nil || (!tc.identityGap && !slices.Contains(got.Gaps, "diff_incomplete")) {
					t.Fatalf("unresolved evidence certified totals: %+v", got)
				}
				return
			}
			if got.Added == nil || *got.Added != int64(17*tc.count) || got.Deleted == nil || *got.Deleted != int64(3*tc.count) || got.Files == nil || *got.Files != int64(2*tc.count) || len(got.Gaps) != 0 {
				t.Fatalf("canonical totals: %+v", got)
			}
		})
	}
}

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
			_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,ticket_node_id,agent_principal_id,harness,host,management,role,work_shape,ref_digest,lease_digest,commits)
 VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',decode(md5('ref'),'hex'),decode(md5('lease'),'hex'),$5::jsonb),
 ($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',decode(md5('ref2'),'hex'),decode(md5('lease2'),'hex'),$5::jsonb)`, p.TenantID, project, ticket, p.ID, `[{"sha":"abcdef0123456789012345678901234567890123","subject":"Measured","lines_added":17,"lines_deleted":3,"files_changed":2}]`)
			return err
		})
		got := read()
		if got.Commits != 1 || got.Added == nil || *got.Added != 17 || *got.Deleted != 3 || *got.Files != 2 {
			t.Fatal(got)
		}
		inTenant(t, d, p, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET commits=commits||'[{"sha":"abcdef1123456789012345678901234567890123","subject":"Unknown"}]'::jsonb WHERE ticket_node_id=$1`, ticket)
			return err
		})
		got = read()
		if got.Commits != 2 || got.Added != nil || got.Deleted != nil || got.Files != nil {
			t.Fatal("missing diff became zero", got)
		}
	})
	var scoped tenant.Principal
	scoped.Kind = tenant.Person
	scoped.TenantID = p.TenantID
	inTenant(t, d, p, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Scoped measurement') RETURNING id::text`, p.TenantID).Scan(&scoped.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, p.TenantID, scoped.ID, project)
		return err
	})
	r := callAs(t, mod, scoped, "GET", "/api/outcomes/measurement?ticket_node_id="+ticket, "")
	if r.Code != 200 {
		t.Fatalf("scoped evidence: %d %s", r.Code, r.Body.String())
	}
	var scopedView ticketMeasurement
	if err := json.Unmarshal(r.Body.Bytes(), &scopedView); err != nil {
		t.Fatal(err)
	}
	if scopedView.Added != nil || scopedView.Deleted != nil || scopedView.Files != nil || !strings.Contains(strings.Join(scopedView.Gaps, ","), "diff_visibility_incomplete") {
		t.Fatal("restricted view certified a diff subset", scopedView)
	}
	foreign := newPerson(t, d, "measurement-foreign")
	if r := callAs(t, mod, foreign, "GET", "/api/outcomes/measurement?ticket_node_id="+ticket, ""); r.Code != 404 {
		t.Fatalf("foreign: %d %s", r.Code, r.Body.String())
	}
	if r := callAs(t, mod, p, "GET", "/api/outcomes/measurement", ""); r.Code != 400 {
		t.Fatalf("missing ticket: %d", r.Code)
	}
}
