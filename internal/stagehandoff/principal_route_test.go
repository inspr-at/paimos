// SPDX-License-Identifier: AGPL-3.0-only
package stagehandoff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

func routedTestRequest(t *testing.T, handler http.Handler, p tenant.Principal, bearer, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(t.Context(), p))
	r.Header.Set("Content-Type", "application/json")
	if strings.HasSuffix(path, "/launch/consume") {
		r.Header.Set("Idempotency-Key", "66666666-6666-4666-8666-666666666666")
	} else {
		r.Header.Set("Idempotency-Key", "44444444-4444-4444-8444-444444444444")
	}
	if bearer != "" {
		r.Header.Set("Authorization", bearer)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func scopedForeignAgent(t *testing.T, m *Module, owner tenant.Principal, release, scope string) (tenant.Principal, string) {
	t.Helper()
	p := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Agent, Name: "foreign"}
	prefix, secret := "foreignroute", "synthetic-route-secret"
	sum := sha256.Sum256([]byte(secret))
	ctx := t.Context()
	err := db.InTenant(dbtest.Seed(ctx), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var human, approval string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='person' LIMIT 1`).Scan(&human); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'agent',$2) RETURNING id::text`, owner.TenantID, p.Name).Scan(&p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1::uuid AND key='member'`, owner.TenantID, p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1::uuid,$2::uuid,'test',$3,$4,ARRAY[$5],(SELECT id FROM principals WHERE tenant_id=$1::uuid AND kind='person' ORDER BY created_at,id LIMIT 1))`, owner.TenantID, p.ID, prefix, hex.EncodeToString(sum[:]), scope); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at) VALUES($1::uuid,$2::uuid,$2::uuid,$3,'node',$4::uuid,'Route regression',now()+interval '1 hour') RETURNING id::text`, owner.TenantID, p.ID, scope, release).Scan(&approval); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1::uuid,$2::uuid,$3::uuid,'approved')`, owner.TenantID, approval, human); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO agent_permission_grants(tenant_id,approval_request_id,agent_principal_id,scope,resource_kind,resource_id,valid_until) SELECT tenant_id,id,agent_principal_id,scope,resource_kind,resource_id,expires_at FROM approval_requests WHERE id=$1::uuid`, approval)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, "Bearer aeon_" + prefix + "_" + secret
}

func fixturePerson(t *testing.T, m *Module, tenantID string) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: tenantID, Kind: tenant.Person}
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text,name FROM principals WHERE kind='person' LIMIT 1`).Scan(&p.ID, &p.Name)
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestJanusApplyWritesRequireRoutedPrincipalThroughServer(t *testing.T) {
	m, janus, project, release, _ := fixture(t)
	foreign, bearer := scopedForeignAgent(t, m, janus, release, "stage.apply")
	person := fixturePerson(t, m, janus.TenantID)
	var handoffID, admissionID string
	ctx := t.Context()
	err := db.InTenant(dbtest.Seed(ctx), m.pool, janus.TenantID, func(tx pgx.Tx) error {
		plan, err := planDigest(ctx, tx, release)
		if err != nil {
			return err
		}
		contextDigest := digest(project, release, "access", "apply", "1", "1", plan, emptyDigest)
		if err := tx.QueryRow(ctx, `INSERT INTO stage_handoffs(tenant_id,project_node_id,release_node_id,stage,operation,plugin_id,requested_by_principal_id,idempotency_key,attempt,authority_epoch,journey_revision,plan_digest,predecessor_digest,context_digest,prerequisite_seal_sha256,evidence_ceiling,expires_at) VALUES($1::uuid,$2::uuid,$3::uuid,'access','apply','janus',$4::uuid,'route-apply',1,1,1,$5,$6,$7,$6,ARRAY['authorization'],now()+interval '30 minutes') RETURNING id::text`, janus.TenantID, project, release, janus.ID, plan, emptyDigest, contextDigest).Scan(&handoffID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO stage_launch_admissions(tenant_id,handoff_id,binding_digest_sha256,artifact_digest_sha256,authority_epoch,expires_at) VALUES($1::uuid,$2::uuid,$3,$3,1,now()+interval '30 minutes') RETURNING id::text`, janus.TenantID, handoffID, emptyDigest).Scan(&admissionID)
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.Server{Modules: []httpapi.Module{m}}).Handler()
	base := "/api/stage-handoffs/" + handoffID
	yes := true
	evidence := EvidenceWrite{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now(), AuthorityEpoch: 1, Authorized: &yes}
	result := ResultWrite{Outcome: "failed", TerminalSequence: 1, AuthorityEpoch: 1, PrerequisiteSealSHA256: emptyDigest, BlockerCode: stringPtr("policy_refused")}
	artifact := Artifact{VersionScheme: "inspr-calendar-v2", Version: "260926000000.0.0", ReleaseChannel: "stable", ReleaseSequence: 1, DigestSHA256: emptyDigest, CommitDigest: "commit", ManifestCoordinate: "manifest", ManifestDigestSHA256: emptyDigest}
	for _, tc := range []struct {
		path string
		body any
	}{
		{base + "/evidence", evidence},
		{base + "/result", result},
		{base + "/launch/admit", artifact},
		{base + "/launch/consume", map[string]string{"admission_id": admissionID}},
	} {
		for _, actor := range []struct {
			name    string
			p       tenant.Principal
			bearer  string
			message string
		}{{"foreign grant holder", foreign, bearer, "routed plugin agent"}, {"person", person, "", "active plugin agent"}} {
			t.Run(strings.TrimPrefix(tc.path, base)+"/"+actor.name, func(t *testing.T) {
				w := routedTestRequest(t, handler, actor.p, actor.bearer, tc.path, tc.body)
				if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), actor.message) {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestJanusPrepareWritesSucceedThroughServer(t *testing.T) {
	m, janus, project, release, bearer := fixture(t)
	var h Handoff
	ctx := t.Context()
	if err := db.InTenant(dbtest.Seed(ctx), m.pool, janus.TenantID, func(tx pgx.Tx) error {
		var err error
		h, err = m.create(ctx, tx, janus, RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: 1, IdempotencyKey: "route-prepare"}, "janus", []string{"authorization"}, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.Server{Modules: []httpapi.Module{m}}).Handler()
	yes := true
	evidence := EvidenceWrite{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now(), AuthorityEpoch: h.AuthorityEpoch, Authorized: &yes}
	base := "/api/stage-handoffs/" + h.ID
	if w := routedTestRequest(t, handler, janus, bearer, base+"/evidence", evidence); w.Code != http.StatusCreated {
		t.Fatalf("evidence: %d %s", w.Code, w.Body.String())
	}
	result := ResultWrite{Outcome: "failed", TerminalSequence: 1, AuthorityEpoch: h.AuthorityEpoch, PrerequisiteSealSHA256: h.PrerequisiteSealSHA256, BlockerCode: stringPtr("policy_refused")}
	if w := routedTestRequest(t, handler, janus, bearer, base+"/result", result); w.Code != http.StatusOK {
		t.Fatalf("result: %d %s", w.Code, w.Body.String())
	}
	if err := db.InTenant(dbtest.Seed(ctx), m.pool, janus.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1::uuid`, janus.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := routedTestRequest(t, handler, janus, bearer, base+"/evidence", evidence); w.Code != http.StatusForbidden {
		t.Fatalf("deactivated agent replay: %d %s", w.Code, w.Body.String())
	}
}

func TestPharosLaunchWritesRequireRoutedPrincipalThroughServer(t *testing.T) {
	w := newLaunchWorld(t)
	w.storeArtifact(t, w.art.DigestSHA256)
	w.postReadiness(t, w.plan, true, true, true, time.Now(), w.h.AuthorityEpoch)
	foreign, bearer := scopedForeignAgent(t, w.m, w.p, w.release, "stage.deploy")
	person := fixturePerson(t, w.m, w.p.TenantID)
	handler := (&httpapi.Server{Modules: []httpapi.Module{w.m}}).Handler()
	base := "/api/stage-handoffs/" + w.h.ID + "/launch/"
	for _, actor := range []struct {
		name    string
		p       tenant.Principal
		bearer  string
		message string
	}{{"foreign grant holder", foreign, bearer, "routed plugin agent"}, {"person", person, "", "active plugin agent"}} {
		resp := routedTestRequest(t, handler, actor.p, actor.bearer, base+"admit", w.art)
		if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), actor.message) {
			t.Fatalf("%s admit: %d %s", actor.name, resp.Code, resp.Body.String())
		}
	}
	admit := routedTestRequest(t, handler, w.p, w.bearer, base+"admit", w.art)
	if admit.Code != http.StatusOK {
		t.Fatalf("routed admit: %d %s", admit.Code, admit.Body.String())
	}
	var admission LaunchAdmission
	if err := json.Unmarshal(admit.Body.Bytes(), &admission); err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"admission_id": admission.ID}
	for _, actor := range []struct {
		name    string
		p       tenant.Principal
		bearer  string
		message string
	}{{"foreign grant holder", foreign, bearer, "routed plugin agent"}, {"person", person, "", "active plugin agent"}} {
		resp := routedTestRequest(t, handler, actor.p, actor.bearer, base+"consume", body)
		if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), actor.message) {
			t.Fatalf("%s consume: %d %s", actor.name, resp.Code, resp.Body.String())
		}
	}
	if resp := routedTestRequest(t, handler, w.p, w.bearer, base+"consume", body); resp.Code != http.StatusOK {
		t.Fatalf("routed consume: %d %s", resp.Code, resp.Body.String())
	}
}
