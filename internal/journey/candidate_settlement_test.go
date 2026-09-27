// SPDX-License-Identifier: AGPL-3.0-only
package journey

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestSettlementPreservesCandidateVersionPin(t *testing.T) {
	for _, tc := range []struct {
		name, evidence string
		conflict       bool
	}{
		{"matching deployment", "260927173806.0.0", false},
		{"no deployment identity", "", false},
		{"different deployment", "260927180000.0.0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := dbtest.Open(t)
			ctx := t.Context()
			var tid, project, release, actor string
			if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('candidate-settlement','Candidate settlement') RETURNING id::text`).Scan(&tid); err != nil {
				t.Fatal(err)
			}
			err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','builder') RETURNING id::text`, tid).Scan(&actor); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'PRJ-1',id,'Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) SELECT $1,'REL-1',id,'Release',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='release' RETURNING id::text`, tid, project).Scan(&release); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, tid, project); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,version_scheme,version) VALUES($1,$2,$3,1,'deploying','inspr-calendar-v2','260927173806.0.0')`, tid, release, project); err != nil {
					return err
				}
				if tc.evidence != "" {
					var handoff string
					hash := strings.Repeat("a", 64)
					if err := tx.QueryRow(ctx, `INSERT INTO stage_handoffs(tenant_id,project_node_id,release_node_id,stage,operation,plugin_id,requested_by_principal_id,idempotency_key,attempt,authority_epoch,journey_revision,plan_digest,predecessor_digest,context_digest,prerequisite_seal_sha256,evidence_ceiling,state,expires_at) VALUES($1,$2,$3,'deploy','deploy','pharos',$4,'settle-fixture',1,1,1,$5,$5,$5,$5,ARRAY['deployment'],'succeeded',now()+interval '30 minutes') RETURNING id::text`, tid, project, release, actor, hash).Scan(&handoff); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `INSERT INTO stage_handoff_evidence(tenant_id,handoff_id,sequence,kind,outcome,observed_at,authority_epoch,workflow,environment,version_scheme,version,release_channel,release_sequence,artifact_digest_sha256,commit_digest,manifest_coordinate,manifest_digest_sha256) VALUES($1,$2,1,'deployment','succeeded',now(),1,'deploy-production','lab','inspr-calendar-v2',$3,'lab',1,$4,$4,'lab:fixture',$4)`, tid, handoff, tc.evidence, hash)
					return err
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error { _, err := settleReleased(ctx, tx, project, release); return err })
			if tc.conflict {
				if err == nil || !strings.Contains(err.Error(), "pinned") {
					t.Fatalf("wanted conflict: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var scheme, version, state string
			if err := d.Admin.QueryRow(ctx, `SELECT version_scheme,version,state FROM journey_releases WHERE release_node_id=$1`, release).Scan(&scheme, &version, &state); err != nil {
				t.Fatal(err)
			}
			if scheme != "inspr-calendar-v2" || version != "260927173806.0.0" {
				t.Fatalf("pin changed: %s %s", scheme, version)
			}
			want := "released"
			if tc.conflict {
				want = "deploying"
			}
			if state != want {
				t.Fatalf("state %s want %s", state, want)
			}
		})
	}
}
