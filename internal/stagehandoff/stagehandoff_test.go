// SPDX-License-Identifier: AGPL-3.0-only
package stagehandoff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/deploytarget"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

const emptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestHandoffCreateAndGetUseStoredExpiry(t *testing.T) {
	m, agent, project, release, _ := fixture(t)
	base := time.Now().UTC().Truncate(time.Second).Add(1477845 * time.Nanosecond)
	m.now = func() time.Time { return base }
	handler := (&httpapi.Server{Modules: []httpapi.Module{m}}).Handler()
	created := routedTestRequest(t, handler, agent, "", "/api/stage-handoffs", RequestWrite{
		ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare",
		ExpectedJourneyRevision: 1, IdempotencyKey: "timestamp-roundtrip",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var handoff Handoff
	if err := json.Unmarshal(created.Body.Bytes(), &handoff); err != nil {
		t.Fatal(err)
	}
	read := httptest.NewRequest(http.MethodGet, "/api/stage-handoffs/"+handoff.ID, nil).
		WithContext(tenant.WithPrincipal(t.Context(), fixturePerson(t, m, agent.TenantID)))
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, read)
	if got.Code != http.StatusOK {
		t.Fatalf("get: %d %s", got.Code, got.Body.String())
	}
	var createFields, getFields map[string]json.RawMessage
	if err := json.Unmarshal(created.Body.Bytes(), &createFields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got.Body.Bytes(), &getFields); err != nil {
		t.Fatal(err)
	}
	if string(createFields["expires_at"]) != string(getFields["expires_at"]) {
		t.Fatalf("create expires_at %s differs from GET %s", createFields["expires_at"], getFields["expires_at"])
	}
	want := base.Add(30 * time.Minute).Truncate(time.Microsecond)
	if !handoff.ExpiresAt.Equal(want) || handoff.ExpiresAt.Nanosecond()%1000 != 0 {
		t.Fatalf("stored expires_at = %s, want %s", handoff.ExpiresAt, want)
	}
}

func TestHandoffInstallationBoundary(t *testing.T) {
	m, p, project, release, _ := fixture(t)
	ctx := t.Context()
	for _, tc := range []struct {
		name        string
		enabled     bool
		digest      string
		permissions []string
		want        bool
	}{
		{"narrow prepare permission", true, "", []string{fence.PermStageAccessPrepare}, true},
		{"disabled", false, "", []string{fence.PermStageAccessPrepare}, false},
		{"wrong digest", true, emptyDigest, []string{fence.PermStageAccessPrepare}, false},
		{"wrong operation permission", true, "", []string{fence.PermStageAccessApply}, false},
		{"legacy permission", true, "", []string{"prepare"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plug, _ := m.registry.Lookup("janus")
			pin := tc.digest
			if pin == "" {
				pin = plug.Manifest.DigestSHA256
			}
			err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE plugin_installations SET enabled=$1,manifest_digest_sha256=$2,permissions=$3 WHERE plugin_id='janus'`, tc.enabled, pin, tc.permissions); err != nil {
					return err
				}
				_, err := m.create(ctx, tx, p, RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: 1, IdempotencyKey: tc.name}, "janus", []string{"authorization", "credential_handoff"}, "")
				return err
			})
			if tc.want {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var rejected *apiError
				if !errors.As(err, &rejected) || rejected.code != 403 {
					t.Fatalf("expected forbidden, got %v", err)
				}
			}
		})
	}
	if err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		for _, plugin := range []string{"pharos", "missing"} {
			ok, err := plugins.Enabled(ctx, tx, m.registry, p.TenantID, plugin, "deploy")
			if err != nil || ok {
				t.Fatalf("uninstalled plugin %s: enabled=%v err=%v", plugin, ok, err)
			}
		}
		for _, operation := range []string{"apply", "unknown"} {
			ok, err := plugins.Enabled(ctx, tx, m.registry, p.TenantID, "janus", operation)
			if err != nil || ok {
				t.Fatalf("uninstalled operation %s: enabled=%v err=%v", operation, ok, err)
			}
		}
		ok, err := plugins.Enabled(ctx, tx, m.registry, "11111111-1111-1111-1111-111111111111", "janus", "prepare")
		if err != nil || ok {
			t.Fatalf("foreign tenant: enabled=%v err=%v", ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExistingHandoffCanFinishAfterPluginDisable(t *testing.T) {
	m, p, project, release, bearer := fixture(t)
	ctx := t.Context()
	err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		h, err := m.create(ctx, tx, p, RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: 1, IdempotencyKey: "before-disable"}, "janus", []string{"authorization", "credential_handoff"}, "")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE plugin_installations SET enabled=false WHERE plugin_id='janus'`); err != nil {
			return err
		}
		yes := true
		for _, evidence := range []EvidenceWrite{
			{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now(), AuthorityEpoch: h.AuthorityEpoch, Authorized: &yes},
			{Sequence: 2, Kind: "credential_handoff", Outcome: "satisfied", ObservedAt: time.Now(), AuthorityEpoch: h.AuthorityEpoch, CredentialReady: &yes},
		} {
			if _, err := m.appendEvidence(ctx, tx, p, bearer, h.ID, evidence); err != nil {
				return err
			}
		}
		result, err := m.close(ctx, tx, p, bearer, h.ID, ResultWrite{Outcome: "succeeded", TerminalSequence: 2, AuthorityEpoch: h.AuthorityEpoch, PrerequisiteSealSHA256: h.PrerequisiteSealSHA256})
		if err == nil && result.Outcome != "succeeded" {
			t.Fatalf("result = %s", result.Outcome)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (*Module, tenant.Principal, string, string, string) {
	t.Helper()
	ctx := context.Background()
	fresh := dbtest.Open(t)
	var tenantID string
	if err := fresh.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('p34','P34') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	registry, err := plugins.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	m := &Module{pool: fresh.App, registry: registry, launchChecks: testLaunchChecks{}}
	var p tenant.Principal
	var project, release string
	err = db.InTenant(dbtest.Seed(ctx), fresh.App, tenantID, func(tx pgx.Tx) error {
		p.TenantID = tenantID
		p.Kind = tenant.Agent
		p.Name = "janus"
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'agent',$2) RETURNING id::text`, tenantID, p.Name).Scan(&p.ID); err != nil {
			return err
		}
		var human string
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Human',ARRAY['admin']) RETURNING id::text`, tenantID).Scan(&human); err != nil {
			return err
		}
		// The worker sees project data only through a binding (ADR-003 P2).
		if err := dbtest.BindLegacyTx(ctx, tx, tenantID, human); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1::uuid AND key='member'`, tenantID, p.ID); err != nil {
			return err
		}
		var projectKind, releaseKind string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM node_kinds WHERE slug='project'`).Scan(&projectKind); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM node_kinds WHERE slug='release'`).Scan(&releaseKind); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) VALUES($1::uuid,'P34-1',$2::uuid,'Project') RETURNING id::text`, tenantID, projectKind).Scan(&project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1::uuid,$2::uuid)`, tenantID, project); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) VALUES($1::uuid,'P34-2',$2::uuid,'Release',$3::uuid) RETURNING id::text`, tenantID, releaseKind, project).Scan(&release); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,access_required) VALUES($1::uuid,$2::uuid,$3::uuid,1,'candidate',true)`, tenantID, release, project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE journey_projects SET current_release_node_id=$1::uuid WHERE project_node_id=$2::uuid`, release, project); err != nil {
			return err
		}
		plug, _ := registry.Lookup("janus")
		man := plug.Manifest
		if _, err := tx.Exec(ctx, `INSERT INTO plugin_installations(tenant_id,plugin_id,version,manifest_digest_sha256,owner,enabled,permissions,updated_by_principal_id) VALUES($1::uuid,$2,$3,$4,$5,true,$6,$7::uuid)`, tenantID, man.ID, man.Version, man.DigestSHA256, man.Owner, man.Permissions, human); err != nil {
			return err
		}
		secret := "fixture-secret"
		sum := sha256.Sum256([]byte(secret))
		prefix := "fixture"
		if _, err := tx.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1::uuid,$2::uuid,'test',$3,$4,ARRAY['stage.prepare','stage.deploy','stage.verify','stage.apply'])`, tenantID, p.ID, prefix, hex.EncodeToString(sum[:])); err != nil {
			return err
		}
		var approval string
		if err := tx.QueryRow(ctx, `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at) VALUES($1::uuid,$2::uuid,$2::uuid,'stage.prepare','node',$3::uuid,'Prepare access',now()+interval '1 hour') RETURNING id::text`, tenantID, p.ID, release).Scan(&approval); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1::uuid,$2::uuid,$3::uuid,'approved')`, tenantID, approval, human); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO agent_permission_grants(tenant_id,approval_request_id,agent_principal_id,scope,resource_kind,resource_id,valid_until) SELECT tenant_id,id,agent_principal_id,scope,resource_kind,resource_id,expires_at FROM approval_requests WHERE id=$1::uuid`, approval)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, p, project, release, "Bearer aeon_fixture_fixture-secret"
}
func routeFixtureAgent(t *testing.T, m *Module, p *tenant.Principal, plugin string) {
	t.Helper()
	ctx := t.Context()
	if err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE principals SET name=$2 WHERE id=$1::uuid`, p.ID, plugin)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p.Name = plugin
}
func TestPrepareHandoffFencingAndEvidence(t *testing.T) {
	m, p, project, release, bearer := fixture(t)
	ctx := context.Background()
	in := RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: 1, IdempotencyKey: "prepare-1"}
	var h Handoff
	err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		h, err = m.create(ctx, tx, p, in, "janus", []string{"authorization", "credential_handoff"}, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Attempt != 1 || h.AuthorityEpoch != 1 || h.PrerequisiteSealSHA256 == "" {
		t.Fatalf("bad handoff: %+v", h)
	}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		same, err := m.create(ctx, tx, p, in, "janus", []string{"authorization", "credential_handoff"}, "")
		if err != nil {
			return err
		}
		if same.ID != h.ID {
			t.Fatal("idempotency replay made a new request")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	e1 := EvidenceWrite{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now().UTC().Truncate(time.Microsecond), AuthorityEpoch: h.AuthorityEpoch, Authorized: &yes}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error { _, err := m.appendEvidence(ctx, tx, p, bearer, h.ID, e1); return err })
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error { _, err := m.appendEvidence(ctx, tx, p, bearer, h.ID, e1); return err })
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	divergent := e1
	no := false
	divergent.Authorized = &no
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error { _, err := m.appendEvidence(ctx, tx, p, bearer, h.ID, divergent); return err })
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.code != 409 {
		t.Fatalf("divergent replay: %v", err)
	}
	e2 := EvidenceWrite{Sequence: 2, Kind: "credential_handoff", Outcome: "satisfied", ObservedAt: time.Now().UTC().Truncate(time.Microsecond), AuthorityEpoch: h.AuthorityEpoch, CredentialReady: &yes}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error { _, err := m.appendEvidence(ctx, tx, p, bearer, h.ID, e2); return err })
	if err != nil {
		t.Fatal(err)
	}
	r := ResultWrite{Outcome: "succeeded", TerminalSequence: 2, AuthorityEpoch: h.AuthorityEpoch, PrerequisiteSealSHA256: h.PrerequisiteSealSHA256}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error { _, err := m.close(ctx, tx, p, bearer, h.ID, r); return err })
	if err != nil {
		t.Fatal(err)
	}
	var eventCount int
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type LIKE 'stage_handoff.%'`).Scan(&eventCount)
	})
	if err != nil {
		t.Fatal(err)
	}
	if eventCount != 4 {
		t.Fatalf("events=%d, want 4", eventCount)
	}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM stage_handoffs WHERE id=$1::uuid`, h.ID).Scan(&state); err != nil {
			return err
		}
		if state != "succeeded" {
			t.Fatalf("handoff state=%s", state)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestRegistryRejectsInvalidManifests(t *testing.T) {
	r := plugins.NewRegistry()
	builtins, err := plugins.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	m, _ := builtins.Lookup("janus")
	if err := r.Register(m); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(m); err == nil {
		t.Fatal("duplicate accepted")
	}
	m.Manifest.ID = "tampered"
	if err := plugins.NewRegistry().Register(m); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}
func TestPharosAdmissionAndTenantFence(t *testing.T) {
	t.Run("legacy", func(t *testing.T) { testPharosOptionalTarget(t, false) })
	t.Run("named", func(t *testing.T) { testPharosOptionalTarget(t, true) })
}
func testPharosOptionalTarget(t *testing.T, named bool) {
	m, p, project, release, bearer := fixture(t)
	handler := (&httpapi.Server{Modules: []httpapi.Module{m}}).Handler()
	ctx := context.Background()
	assertInternalTarget := func(h Handoff) {
		t.Helper()
		err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
			stored, err := loadHandoff(ctx, tx, h.ID, false)
			if err != nil {
				return err
			}
			if stored.TargetDigestSHA256 != h.TargetDigestSHA256 || (stored.Target != nil) != named {
				t.Fatal("stored target changed")
			}
			var eventTarget bool
			if err := tx.QueryRow(ctx, `SELECT after ? 'target' AND after ? 'target_digest_sha256' FROM events WHERE type='stage_handoff.requested' AND after->>'id'=$1`, h.ID).Scan(&eventTarget); err != nil {
				return err
			}
			if eventTarget != named {
				t.Fatal("audit target changed")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		read := httptest.NewRequest(http.MethodGet, "/api/stage-handoffs/"+h.ID, nil).
			WithContext(tenant.WithPrincipal(t.Context(), fixturePerson(t, m, p.TenantID)))
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, read)
		if got.Code != http.StatusOK {
			t.Fatalf("get: %d %s", got.Code, got.Body.String())
		}
		if got.Header().Get("Aeon-Contract") != "stage-handoffs/1.0" {
			t.Fatal("handoff version changed")
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"target", "target_digest_sha256"} {
			if _, ok := body[field]; ok {
				t.Fatalf("PHAROS response contains %s", field)
			}
		}
	}
	err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var human string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='person' LIMIT 1`).Scan(&human); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE journey_releases SET version_scheme='inspr-calendar-v2',version='260923000000.0.0' WHERE release_node_id=$1::uuid`, release); err != nil {
			return err
		}
		plug, _ := m.registry.Lookup("pharos")
		man := plug.Manifest
		if _, err := tx.Exec(ctx, `INSERT INTO plugin_installations(tenant_id,plugin_id,version,manifest_digest_sha256,owner,enabled,permissions,updated_by_principal_id) VALUES($1::uuid,$2,$3,$4,$5,true,$6,$7::uuid)`, p.TenantID, man.ID, man.Version, man.DigestSHA256, man.Owner, man.Permissions, human); err != nil {
			return err
		}
		for _, gate := range []string{"candidate", "deploy", "access"} {
			var approval string
			scope := "stage." + gate
			if gate == "access" {
				scope = "stage.apply"
			}
			if err := tx.QueryRow(ctx, `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at) VALUES($1::uuid,$2::uuid,$2::uuid,$3,'node',$4::uuid,'Reviewed',now()+interval '1 hour') RETURNING id::text`, p.TenantID, p.ID, scope, release).Scan(&approval); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1::uuid,$2::uuid,$3::uuid,'approved')`, p.TenantID, approval, human); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO agent_permission_grants(tenant_id,approval_request_id,agent_principal_id,scope,resource_kind,resource_id,valid_until) SELECT tenant_id,id,agent_principal_id,scope,resource_kind,resource_id,expires_at FROM approval_requests WHERE id=$1::uuid`, approval); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid)`, p.TenantID, project, release, gate, approval); err != nil {
				return err
			}
		}
		var verifyApproval string
		if err := tx.QueryRow(ctx, `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at) VALUES($1::uuid,$2::uuid,$2::uuid,'stage.verify','node',$3::uuid,'Verify',now()+interval '1 hour') RETURNING id::text`, p.TenantID, p.ID, release).Scan(&verifyApproval); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1::uuid,$2::uuid,$3::uuid,'approved')`, p.TenantID, verifyApproval, human); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_permission_grants(tenant_id,approval_request_id,agent_principal_id,scope,resource_kind,resource_id,valid_until) SELECT tenant_id,id,agent_principal_id,scope,resource_kind,resource_id,expires_at FROM approval_requests WHERE id=$1::uuid`, verifyApproval); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prepareRequest := RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: 1, IdempotencyKey: "prepare-before-deploy"}
	var prepare Handoff
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		prepare, err = m.create(ctx, tx, p, prepareRequest, "janus", []string{"authorization", "credential_handoff"}, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	for _, e := range []EvidenceWrite{{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now().UTC(), AuthorityEpoch: prepare.AuthorityEpoch, Authorized: &yes}, {Sequence: 2, Kind: "credential_handoff", Outcome: "satisfied", ObservedAt: time.Now().UTC(), AuthorityEpoch: prepare.AuthorityEpoch, CredentialReady: &yes}} {
		if resp := routedTestRequest(t, handler, p, bearer, "/api/stage-handoffs/"+prepare.ID+"/evidence", e); resp.Code != http.StatusCreated {
			t.Fatalf("Janus prepare evidence: %d %s", resp.Code, resp.Body.String())
		}
	}
	if resp := routedTestRequest(t, handler, p, bearer, "/api/stage-handoffs/"+prepare.ID+"/result", ResultWrite{Outcome: "succeeded", TerminalSequence: 2, AuthorityEpoch: prepare.AuthorityEpoch, PrerequisiteSealSHA256: prepare.PrerequisiteSealSHA256}); resp.Code != http.StatusOK {
		t.Fatalf("Janus prepare result: %d %s", resp.Code, resp.Body.String())
	}
	in := RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "deploy", Operation: "deploy", ExpectedJourneyRevision: 1, IdempotencyKey: "deploy-1"}
	if named {
		in.Target, in.TargetDigestSHA256, err = deploytarget.Normalize(&deploytarget.Target{Hosts: []string{"edge-1"}, Environment: "production", Service: "pharos", Change: "Update image"})
		if err != nil {
			t.Fatal(err)
		}
	}

	var h Handoff
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		h, err = m.create(ctx, tx, p, in, "pharos", []string{"deployment"}, "deploy")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertInternalTarget(h)
	routeFixtureAgent(t, m, &p, "pharos")
	a := Artifact{VersionScheme: "inspr-calendar-v2", Version: "260923000000.0.0", ReleaseChannel: "stable", ReleaseSequence: 1, DigestSHA256: emptyDigest, CommitDigest: "commit", ManifestCoordinate: "manifest", ManifestDigestSHA256: emptyDigest}
	e := EvidenceWrite{Sequence: 1, Kind: "deployment", Outcome: "succeeded", ObservedAt: time.Now().UTC(), AuthorityEpoch: h.AuthorityEpoch, Workflow: stringPtr("deploy"), Environment: stringPtr("production"), Artifact: &a}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error { _, err := m.appendEvidence(ctx, tx, p, bearer, h.ID, e); return err })
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.code != 409 {
		t.Fatalf("launch without admission: %v", err)
	}
	m.launchChecks = nil
	if _, err := m.AdmitLaunch(ctx, p, bearer, h.ID, a); !errors.As(err, &apiErr) || apiErr.code != 409 {
		t.Fatalf("missing readiness provider: %v", err)
	}
	m.launchChecks = testLaunchChecks{}
	if err := m.ConsumeLaunch(ctx, p, bearer, h.ID, "00000000-0000-4000-8000-000000000099"); !errors.As(err, &apiErr) || apiErr.code != 404 {
		t.Fatalf("consume without admit: %v", err)
	}
	admission, err := m.AdmitLaunch(ctx, p, bearer, h.ID, a)
	if err != nil {
		t.Fatal(err)
	}
	replayedAdmission, err := m.AdmitLaunch(ctx, p, bearer, h.ID, a)
	if err != nil || replayedAdmission.ID != admission.ID {
		t.Fatalf("admit replay: %v %+v", err, replayedAdmission)
	}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='stage_handoff.launch_admitted'`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("admit replay events=%d", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	consumeErrs := make([]error, 2)
	var consumeWG sync.WaitGroup
	consumeStart := make(chan struct{})
	for i := 0; i < 2; i++ {
		consumeWG.Add(1)
		go func(i int) {
			defer consumeWG.Done()
			<-consumeStart
			consumeErrs[i] = m.ConsumeLaunch(ctx, p, bearer, h.ID, admission.ID)
		}(i)
	}
	close(consumeStart)
	consumeWG.Wait()
	wins := 0
	for _, consumeErr := range consumeErrs {
		if consumeErr == nil {
			wins++
			continue
		}
		if !errors.As(consumeErr, &apiErr) || apiErr.code != 409 {
			t.Fatalf("concurrent consume: %v", consumeErr)
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent consume wins=%d", wins)
	}
	if resp := routedTestRequest(t, handler, p, bearer, "/api/stage-handoffs/"+h.ID+"/evidence", e); resp.Code != http.StatusCreated {
		t.Fatalf("Pharos deploy evidence: %d %s", resp.Code, resp.Body.String())
	}
	if resp := routedTestRequest(t, handler, p, bearer, "/api/stage-handoffs/"+h.ID+"/result", ResultWrite{Outcome: "succeeded", TerminalSequence: 1, AuthorityEpoch: h.AuthorityEpoch, PrerequisiteSealSHA256: h.PrerequisiteSealSHA256}); resp.Code != http.StatusOK {
		t.Fatalf("Pharos deploy result: %d %s", resp.Code, resp.Body.String())
	}
	if _, err := m.AdmitLaunch(ctx, p, bearer, h.ID, a); !errors.As(err, &apiErr) || apiErr.code != 409 {
		t.Fatalf("admit after close: %v", err)
	}
	verifyRequest := RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "deploy", Operation: "verify", ExpectedJourneyRevision: 2, IdempotencyKey: "verify-1"}
	var verification Handoff
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		verification, err = m.create(ctx, tx, p, verifyRequest, "pharos", []string{"verification"}, "deploy")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if verification.TargetDigestSHA256 != h.TargetDigestSHA256 {
		t.Fatal("verify lost deploy digest")
	}
	if named && (verification.Target == nil || verification.Target.Hosts[0] != "edge-1") {
		t.Fatal("verify lost deploy target")
	}
	if !named && verification.Target != nil {
		t.Fatal("invented verify target")
	}
	assertInternalTarget(verification)
	verificationEvidence := EvidenceWrite{Sequence: 1, Kind: "verification", Outcome: "succeeded", ObservedAt: time.Now().UTC(), AuthorityEpoch: verification.AuthorityEpoch, Workflow: stringPtr("verify"), Environment: stringPtr("production"), Artifact: &a}
	if resp := routedTestRequest(t, handler, p, bearer, "/api/stage-handoffs/"+verification.ID+"/evidence", verificationEvidence); resp.Code != http.StatusCreated {
		t.Fatalf("Pharos verify evidence: %d %s", resp.Code, resp.Body.String())
	}
	if resp := routedTestRequest(t, handler, p, bearer, "/api/stage-handoffs/"+verification.ID+"/result", ResultWrite{Outcome: "succeeded", TerminalSequence: 1, AuthorityEpoch: verification.AuthorityEpoch, PrerequisiteSealSHA256: verification.PrerequisiteSealSHA256}); resp.Code != http.StatusOK {
		t.Fatalf("Pharos verify result: %d %s", resp.Code, resp.Body.String())
	}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var state string
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT state FROM journey_releases WHERE release_node_id=$1::uuid`, release).Scan(&state); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT revision FROM journey_projects WHERE project_node_id=$1::uuid`, project).Scan(&revision); err != nil {
			return err
		}
		if state != "access" || revision != 3 {
			t.Fatalf("release=%s journey revision=%d", state, revision)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	routeFixtureAgent(t, m, &p, "janus")
	applyRequest := RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "apply", ExpectedJourneyRevision: 3, IdempotencyKey: "apply-1"}
	var apply Handoff
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		apply, err = m.create(ctx, tx, p, applyRequest, "janus", []string{"authorization", "credential_handoff"}, "access")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []EvidenceWrite{{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now().UTC(), AuthorityEpoch: apply.AuthorityEpoch, Authorized: &yes}, {Sequence: 2, Kind: "credential_handoff", Outcome: "satisfied", ObservedAt: time.Now().UTC(), AuthorityEpoch: apply.AuthorityEpoch, CredentialReady: &yes}} {
		if resp := routedTestRequest(t, handler, p, bearer, "/api/stage-handoffs/"+apply.ID+"/evidence", e); resp.Code != http.StatusCreated {
			t.Fatalf("Janus apply evidence: %d %s", resp.Code, resp.Body.String())
		}
	}
	if resp := routedTestRequest(t, handler, p, bearer, "/api/stage-handoffs/"+apply.ID+"/result", ResultWrite{Outcome: "succeeded", TerminalSequence: 2, AuthorityEpoch: apply.AuthorityEpoch, PrerequisiteSealSHA256: apply.PrerequisiteSealSHA256}); resp.Code != http.StatusOK {
		t.Fatalf("Janus apply result: %d %s", resp.Code, resp.Body.String())
	}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM journey_releases WHERE release_node_id=$1::uuid`, release).Scan(&state); err != nil {
			return err
		}
		if state != "released" {
			t.Fatalf("final release state=%s", state)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		replayed, err := m.create(ctx, tx, p, in, "pharos", []string{"deployment"}, "deploy")
		if err != nil {
			return err
		}
		if replayed.ID != h.ID {
			t.Fatal("completed request replay created another handoff")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	otherTenant := "00000000-0000-0000-0000-000000000001"
	err = db.InTenant(dbtest.Seed(ctx), m.pool, otherTenant, func(tx pgx.Tx) error { _, err := loadHandoff(ctx, tx, h.ID, false); return err })
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant handoff visible: %v", err)
	}
}
func stringPtr(s string) *string { return &s }

type testLaunchChecks struct{}

func (testLaunchChecks) CheckLaunch(_ context.Context, _, _, _ string, a Artifact) (LaunchReadiness, error) {
	return LaunchReadiness{ReviewedArtifactDigestSHA256: a.DigestSHA256, BackupReady: true, HostReady: true, ObservedAt: time.Now()}, nil
}
func TestNewAttemptRevokesOldAuthority(t *testing.T) {
	m, p, project, release, bearer := fixture(t)
	ctx := context.Background()
	request := RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: 1, IdempotencyKey: "first"}
	var first, second Handoff
	err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		first, err = m.create(ctx, tx, p, request, "janus", []string{"authorization", "credential_handoff"}, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "second"
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		second, err = m.create(ctx, tx, p, request, "janus", []string{"authorization", "credential_handoff"}, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.AuthorityEpoch <= first.AuthorityEpoch {
		t.Fatal("authority did not advance")
	}
	yes := true
	e := EvidenceWrite{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now().UTC(), AuthorityEpoch: first.AuthorityEpoch, Authorized: &yes}
	err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error { _, err := m.appendEvidence(ctx, tx, p, bearer, first.ID, e); return err })
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.code != 409 {
		t.Fatalf("old reporter was not fenced: %v", err)
	}
}
