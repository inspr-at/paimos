// SPDX-License-Identifier: AGPL-3.0-only
package stagehandoff

import (
	"fmt"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestTerminalHandoffsSettleSuccessiveReleases(t *testing.T) {
	for _, operation := range []string{"verify", "apply"} {
		t.Run(operation, func(t *testing.T) {
			t.Setenv("AEON_STATUS_AUTOPILOT", "on")
			m, p, project, first, bearer := fixture(t)
			ctx := t.Context()
			change := func(fn func(pgx.Tx) error) {
				t.Helper()
				if err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, fn); err != nil {
					t.Fatal(err)
				}
			}
			plugin := "pharos"
			stage := "deploy"
			gate := "deploy"
			state := "deploying"
			if operation == "apply" {
				plugin = "janus"
				stage = "access"
				gate = "access"
				state = "access"
			}
			routeFixtureAgent(t, m, &p, plugin)
			var human string
			change(func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='person' LIMIT 1`).Scan(&human); err != nil {
					return err
				}
				plug, _ := m.registry.Lookup(plugin)
				man := plug.Manifest
				_, err := tx.Exec(ctx, `INSERT INTO plugin_installations(tenant_id,plugin_id,version,manifest_digest_sha256,owner,enabled,permissions,updated_by_principal_id) VALUES($1,$2,$3,$4,$5,true,$6,$7) ON CONFLICT(tenant_id,plugin_id) DO NOTHING`, p.TenantID, man.ID, man.Version, man.DigestSHA256, man.Owner, man.Permissions, human)
				return err
			})
			var second string
			for number := 1; number <= 2; number++ {
				release := first
				change(func(tx pgx.Tx) error {
					if number == 2 {
						if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'REL-2','Second release',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='release' RETURNING nodes.id::text`, p.TenantID, project).Scan(&release); err != nil {
							return err
						}
						second = release
						if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,2)`, p.TenantID, release, project); err != nil {
							return err
						}
					}
					if _, err := tx.Exec(ctx, `UPDATE journey_releases SET state=$2,access_required=$3 WHERE release_node_id=$1`, release, state, operation == "apply"); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `UPDATE journey_projects SET current_release_node_id=$2 WHERE project_node_id=$1`, project, release); err != nil {
						return err
					}
					var approval string
					if err := tx.QueryRow(ctx, `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at) VALUES($1,$2,$2,$3,'node',$4,'Reviewed',now()+interval '1 hour') RETURNING id::text`, p.TenantID, p.ID, "stage."+operation, release).Scan(&approval); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1,$2,$3,'approved')`, p.TenantID, approval, human); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO agent_permission_grants(tenant_id,approval_request_id,agent_principal_id,scope,resource_kind,resource_id,valid_until) SELECT tenant_id,id,agent_principal_id,scope,resource_kind,resource_id,expires_at FROM approval_requests WHERE id=$1`, approval); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id) VALUES($1,$2,$3,$4,$5)`, p.TenantID, project, release, gate, approval); err != nil {
						return err
					}
					plan, err := planDigest(ctx, tx, release)
					if err != nil {
						return err
					}
					var deploy string
					if err := tx.QueryRow(ctx, `INSERT INTO stage_handoffs(tenant_id,project_node_id,release_node_id,stage,operation,plugin_id,requested_by_principal_id,idempotency_key,attempt,authority_epoch,journey_revision,plan_digest,predecessor_digest,context_digest,prerequisite_seal_sha256,evidence_ceiling,state,expires_at) VALUES($1,$2,$3,'deploy','deploy','pharos',$4,('prior-deployment-'||($3::uuid)::text),1,1,1,$5,$6,$6,$6,ARRAY['deployment'],'succeeded',now()+interval '1 hour') RETURNING id::text`, p.TenantID, project, release, p.ID, plan, emptyDigest).Scan(&deploy); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO stage_handoff_evidence(tenant_id,handoff_id,sequence,kind,outcome,observed_at,authority_epoch,workflow,environment,version_scheme,version,release_channel,release_sequence,artifact_digest_sha256,commit_digest,manifest_coordinate,manifest_digest_sha256) VALUES($1,$2,1,'deployment','succeeded',now(),1,'deploy','test','inspr-calendar-v2',$3,'stable',$4,$5,'commit','manifest',$5)`, p.TenantID, deploy, fmt.Sprintf("26092300000%d.0.0", number), number, emptyDigest); err != nil {
						return err
					}
					_, err = tx.Exec(ctx, `INSERT INTO stage_handoff_results(tenant_id,handoff_id,outcome,terminal_sequence,authority_epoch,prerequisite_seal_sha256) VALUES($1,$2,'succeeded',1,1,$3)`, p.TenantID, deploy, emptyDigest)
					return err
				})
				var h Handoff
				change(func(tx pgx.Tx) error {
					var rev int64
					if err := tx.QueryRow(ctx, `SELECT revision FROM journey_projects WHERE project_node_id=$1`, project).Scan(&rev); err != nil {
						return err
					}
					var err error
					h, err = m.create(ctx, tx, p, RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: stage, Operation: operation, ExpectedJourneyRevision: rev, IdempotencyKey: fmt.Sprint("terminal-", number)}, plugin, []string{"authorization", "credential_handoff", "verification"}, gate)
					return err
				})
				evidence := []EvidenceWrite{{Sequence: 1, Kind: "verification", Outcome: "succeeded", ObservedAt: time.Now(), AuthorityEpoch: h.AuthorityEpoch, Workflow: stringPtr("verify"), Environment: stringPtr("test"), Artifact: &Artifact{VersionScheme: "inspr-calendar-v2", Version: fmt.Sprintf("26092300000%d.0.0", number), ReleaseChannel: "stable", ReleaseSequence: int64(number), DigestSHA256: emptyDigest, CommitDigest: "commit", ManifestCoordinate: "manifest", ManifestDigestSHA256: emptyDigest}}}
				if operation == "apply" {
					yes := true
					evidence = []EvidenceWrite{{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now(), AuthorityEpoch: h.AuthorityEpoch, Authorized: &yes}, {Sequence: 2, Kind: "credential_handoff", Outcome: "satisfied", ObservedAt: time.Now(), AuthorityEpoch: h.AuthorityEpoch, CredentialReady: &yes}}
				}
				for _, e := range evidence {
					change(func(tx pgx.Tx) error { _, err := m.appendEvidence(ctx, tx, p, bearer, h.ID, e); return err })
				}
				result := ResultWrite{Outcome: "succeeded", TerminalSequence: int64(len(evidence)), AuthorityEpoch: h.AuthorityEpoch, PrerequisiteSealSHA256: h.PrerequisiteSealSHA256}
				change(func(tx pgx.Tx) error { _, err := m.close(ctx, tx, p, bearer, h.ID, result); return err })
				change(func(tx pgx.Tx) error {
					var version string
					var published bool
					var revision int64
					if err := tx.QueryRow(ctx, `SELECT coalesce(version,''),revision,EXISTS(SELECT 1 FROM status_autopilot_releases WHERE release_id=r.release_node_id) FROM journey_releases r WHERE release_node_id=$1`, release).Scan(&version, &revision, &published); err != nil {
						return err
					}
					if version != fmt.Sprintf("26092300000%d.0.0", number) || !published {
						t.Errorf("settlement skipped version/publication: version=%q published=%t", version, published)
					}
					if _, err := m.close(ctx, tx, p, bearer, h.ID, result); err != nil {
						return err
					}
					var replayRevision int64
					if err := tx.QueryRow(ctx, `SELECT revision FROM journey_releases WHERE release_node_id=$1`, release).Scan(&replayRevision); err != nil {
						return err
					}
					if revision != replayRevision {
						t.Error("result replay settled twice")
					}
					return nil
				})
			}
			change(func(tx pgx.Tx) error {
				var old, live string
				if err := tx.QueryRow(ctx, `SELECT a.state,b.state FROM journey_releases a CROSS JOIN journey_releases b WHERE a.release_node_id=$1 AND b.release_node_id=$2`, first, second).Scan(&old, &live); err != nil {
					return err
				}
				if old != "superseded" || live != "released" {
					t.Errorf("two handoff releases: old=%s current=%s", old, live)
				}
				return nil
			})
		})
	}
}
