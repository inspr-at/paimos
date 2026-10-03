// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestDeliverySnapshotImmutabilityBoundsAndReleasedOutcomes(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-snapshots")
	f.exec(t, `UPDATE project_releases SET state='frozen' WHERE release_node_id=$1`, f.releaseA)
	f.exec(t, `UPDATE project_releases SET version='1.0.0',version_scheme='legacy',cut_at=now() WHERE release_node_id=$1`, f.releaseA)
	f.exec(t, `UPDATE project_releases SET state='released',released_at=now(),reservation_basis='attested',released_by=$2 WHERE release_node_id=$1`, f.releaseA, f.actor)
	snapshot := map[string]any{
		"schema": "aeon.release-note-snapshot.v1", "tenant_id": f.tenant, "project_node_id": f.projectA,
		"release_node_id": f.releaseA, "version": "1.0.0", "version_scheme": "legacy", "release_revision": 1,
		"captured_at": "2026-10-03T00:00:00Z", "membership_source": "ships_in.release_node_id",
		"field_source": "nodes.fields", "frozen": true,
		"tickets": []any{map[string]any{"id": f.ticketA, "key": "TA-1"}, map[string]any{"id": f.task, "key": "TSK-1"}},
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		q := `INSERT INTO project_release_note_snapshots(tenant_id,project_node_id,release_node_id,version,snapshot) VALUES($1,$2,$3,'1.0.0',$4::jsonb)`
		deliveryReject(t, ctx, tx, "23514", "snapshot_check", q, f.tenant, f.projectA, f.releaseA, `{}`)
		deliveryReject(t, ctx, tx, "23514", "snapshot_check", `INSERT INTO project_release_note_snapshots(tenant_id,project_node_id,release_node_id,version,snapshot) VALUES($1,$2,$3,'1.0.0',jsonb_set($4::jsonb,'{tickets}','{}'::jsonb))`, f.tenant, f.projectA, f.releaseA, string(b))
		deliveryReject(t, ctx, tx, "23514", "snapshot_check", `INSERT INTO project_release_note_snapshots(tenant_id,project_node_id,release_node_id,version,snapshot) VALUES($1,$2,$3,'1.0.0',jsonb_set($4::jsonb,'{version}','"2.0.0"'::jsonb))`, f.tenant, f.projectA, f.releaseA, string(b))
		deliveryReject(t, ctx, tx, "23514", "snapshot_check", `INSERT INTO project_release_note_snapshots(tenant_id,project_node_id,release_node_id,version,snapshot) VALUES($1,$2,$3,'1.0.0',jsonb_set($4::jsonb,'{tickets}',(SELECT jsonb_agg('{}'::jsonb) FROM generate_series(1,5001))))`, f.tenant, f.projectA, f.releaseA, string(b))
		deliveryReject(t, ctx, tx, "23514", "snapshot_check", `INSERT INTO project_release_note_snapshots(tenant_id,project_node_id,release_node_id,version,snapshot) VALUES($1,$2,$3,'1.0.0',$4::jsonb||jsonb_build_object('oversized',repeat('a',12582913)))`, f.tenant, f.projectA, f.releaseA, string(b))
		// Stored project is bound to the release FK as well as the JSON header.
		deliveryReject(t, ctx, tx, "23503", "foreign key constraint", `INSERT INTO project_release_note_snapshots(tenant_id,project_node_id,release_node_id,version,snapshot) VALUES($1,$2,$3,'1.0.0',jsonb_set($4::jsonb,'{project_node_id}',to_jsonb($2::text)))`, f.tenant, f.projectB, f.releaseA, string(b))
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.principal_ids',$1,true)`, "{"+f.actor+"}"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, q, f.tenant, f.projectA, f.releaseA, string(b)); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "23505", "project_release_note_snapshots_pkey", q, f.tenant, f.projectA, f.releaseA, string(b))
		deliveryReject(t, ctx, tx, "P0001", "snapshots are immutable", `UPDATE project_release_note_snapshots SET version='2.0.0' WHERE release_node_id=$1`, f.releaseA)
		deliveryReject(t, ctx, tx, "P0001", "snapshots are immutable", `DELETE FROM project_release_note_snapshots WHERE release_node_id=$1`, f.releaseA)
		var outcomes int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM outcome_events WHERE kind='released' AND release_node_id=$1 AND ticket_node_id=ANY($2::uuid[]) AND payload->>'version'='1.0.0' AND payload->>'version_scheme'='legacy'`, f.releaseA, []string{f.ticketA, f.task}).Scan(&outcomes); err != nil {
			return err
		}
		if outcomes != 2 {
			t.Fatalf("reused outcome trigger recorded %d, want ticket and task", outcomes)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgrelid='project_release_note_snapshots'::regclass AND tgfoid IN ('aeon_release_note_snapshots_immutable()'::regprocedure,'aeon_record_released_snapshot()'::regprocedure)`).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			t.Fatalf("existing functions not reused: %d", count)
		}
		return nil
	})
}

func TestDeliveryAdoptionCheckpointRequiresBoundRecoveryEvidence(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-evidence")
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		// A successful checkpoint is possible only with complete same-attempt proof.
		if _, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET
			attempts=1,attempt_id=gen_random_uuid(),attempt_started_at=now(),attempt_deadline_at=now()+interval '15 minutes',
			source_fingerprint=repeat('a',64),report_ref='report:local',report_digest=repeat('b',64),
			backup_ref='backup:local',backup_digest=repeat('c',64),restore_evidence_ref='restore:local',restore_evidence_digest=repeat('d',64),
			recovery_pin_manifest_ref='pin:protected',lease_generation=1,lease_token=gen_random_uuid(),lease_until=now()+interval '1 minute'
			WHERE project_node_id=$1`, f.projectA); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET evidence_attempt_id=attempt_id,backup_verified_at=now() WHERE project_node_id=$1`, f.projectA); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "23514", "check constraint", `UPDATE delivery_adoption_jobs SET state='applying',evidence_attempt_id=gen_random_uuid() WHERE project_node_id=$1`, f.projectA)
		deliveryReject(t, ctx, tx, "23514", "check constraint", `UPDATE delivery_adoption_jobs SET state='applying',report_incomplete=true WHERE project_node_id=$1`, f.projectA)
		deliveryReject(t, ctx, tx, "23514", "check constraint", `UPDATE delivery_adoption_jobs SET state='applying',recovery_pin_manifest_ref=NULL WHERE project_node_id=$1`, f.projectA)
		if _, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET state='applying' WHERE project_node_id=$1`, f.projectA); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET state='adopted',adopted_at=now(),cleanup_state='pending',next_reconcile_at=now(),lease_token=NULL,lease_until=NULL WHERE project_node_id=$1`, f.projectA); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM delivery_adoption_jobs WHERE state='adopted' AND next_reconcile_at IS NOT NULL AND cleanup_state='pending'`).Scan(&pending); err != nil {
			return err
		}
		if pending != 1 {
			t.Fatal("terminal checkpoint lost independent cleanup scheduling")
		}
		return nil
	})
}
