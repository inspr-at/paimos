// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestWorkMetadataMigrationBackfillsOnlyUniqueSessions(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tenantIDs, profileIDs []string
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1063_work_classification_model_identity.sql" {
			return nil
		}
		for i := 0; i < 2; i++ {
			var tenantID, profileID string
			if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Identity upgrade') RETURNING id::text`, fmt.Sprintf("identity-upgrade-%d", i)).Scan(&tenantID); err != nil {
				return err
			}
			err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'upgrade-model','1','grok','xai','upgrade-model','xhigh','standard') RETURNING id::text`, tenantID).Scan(&profileID); err != nil {
					return err
				}
				var principal, project string
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','upgrade-agent') RETURNING id::text`, tenantID).Scan(&principal); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'UPG-1','Upgrade' FROM node_kinds WHERE slug='project' RETURNING id::text`, tenantID).Scan(&project); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields) SELECT $1,id,'UPG-2','Historical ticket',$2,'{"estimate_hours":4}'::jsonb FROM node_kinds WHERE slug='ticket'`, tenantID, project); err != nil {
					return err
				}
				for _, model := range []string{"upgrade-model-xhigh", "unknown-upgrade-xhigh"} {
					if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,model) VALUES($1,$2,$3,'grok','test','unmanaged','worker',convert_to($4,'UTF8'),convert_to($4,'UTF8'),$4)`, tenantID, project, principal, model); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			tenantIDs = append(tenantIDs, tenantID)
			profileIDs = append(profileIDs, profileID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, tenantID := range tenantIDs {
		err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
			var model, effort, raw, profile string
			if err := tx.QueryRow(t.Context(), `SELECT model,reasoning_effort,model_raw,model_profile_id::text FROM harness_sessions WHERE model_profile_id IS NOT NULL`).Scan(&model, &effort, &raw, &profile); err != nil {
				return err
			}
			if model != "upgrade-model" || effort != "xhigh" || raw != "upgrade-model-xhigh" || profile != profileIDs[i] {
				t.Fatalf("wrong identity: %s/%s raw=%s profile=%s", model, effort, raw, profile)
			}
			var unknown string
			var missingEffort, missingRaw, missingProfile *string
			if err := tx.QueryRow(t.Context(), `SELECT model,reasoning_effort,model_raw,model_profile_id::text FROM harness_sessions WHERE model_profile_id IS NULL`).Scan(&unknown, &missingEffort, &missingRaw, &missingProfile); err != nil {
				return err
			}
			if unknown != "unknown-upgrade-xhigh" || missingEffort != nil || missingRaw != nil || missingProfile != nil {
				t.Fatal("unknown history changed")
			}
			var fields []byte
			if err := tx.QueryRow(t.Context(), `SELECT fields FROM nodes WHERE key='UPG-2'`).Scan(&fields); err != nil {
				return err
			}
			var old map[string]any
			if err := json.Unmarshal(fields, &old); err != nil {
				return err
			}
			if len(old) != 1 || old["estimate_hours"] != float64(4) {
				t.Fatalf("ticket backfilled: %s", fields)
			}
			var n int
			if err := tx.QueryRow(t.Context(), `SELECT aeon_backfill_session_model_profiles()`).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatalf("repeat backfill changed %d sessions", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal("repeat migration:", err)
	}
}
