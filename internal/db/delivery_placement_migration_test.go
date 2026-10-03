// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestDeliveryPlacementAdmissionAndTombstoneMaintenance(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-g2")
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		deliveryReject(t, ctx, tx, "P0001", "adopted project", `UPDATE ships_in SET project_node_id=$2 WHERE item_node_id=$1`, f.ticketA, f.projectB)
		deliveryReject(t, ctx, tx, "23503", "foreign key constraint", `UPDATE ships_in SET release_node_id=$2 WHERE item_node_id=$1`, f.ticketA, f.releaseB)
		deliveryReject(t, ctx, tx, "23505", "ships_in_pkey", `INSERT INTO ships_in(tenant_id,item_node_id,project_node_id,rank,source,placed_by) VALUES($1,$2,$3,'W','person',$4)`, f.tenant, f.ticketA, f.projectA, f.actor)
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, f.task); err != nil {
			return err
		}
		q := `INSERT INTO ships_in(tenant_id,item_node_id,project_node_id,release_node_id,rank,source,placed_by) VALUES($1,$2,$3,$4,'W',$5,$6)`
		deliveryReject(t, ctx, tx, "P0001", "live epic, ticket or task", q, f.tenant, f.task, f.projectA, f.releaseA, "person", f.actor)
		if _, err := tx.Exec(ctx, q, f.tenant, f.task, f.projectA, f.releaseA, "adopted", f.actor); err != nil {
			return err
		}
		// Cut/abandon can relocate both a tombstone and a live row, retaining both.
		if _, err := tx.Exec(ctx, `UPDATE ships_in SET release_node_id=$2,rank='W',source='person' WHERE item_node_id=$1`, f.task, f.nextRelease); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, f.task); err != nil {
			return err
		}
		var target string
		if err := tx.QueryRow(ctx, `SELECT release_node_id::text FROM ships_in WHERE item_node_id=$1`, f.task).Scan(&target); err != nil {
			return err
		}
		if target != f.nextRelease {
			t.Fatal("restore lost tombstone membership")
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, f.task); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "P0001", "live epic, ticket or task", `UPDATE ships_in SET item_node_id=item_node_id WHERE item_node_id=$1`, f.task)
		// The adopted exception skips liveness only, never permitted-kind checks.
		other := insertNode(ctx, t, tx, f.visibilityFixture, "memory", "MEM-1", &f.projectA)
		deliveryReject(t, ctx, tx, "P0001", "live epic, ticket or task", q, f.tenant, other, f.projectA, f.releaseA, "adopted", f.actor)
		// A hidden backlog row can change kind, and its later maintenance still works.
		backlog := insertNode(ctx, t, tx, f.visibilityFixture, "ticket", "BACK-1", &f.projectA)
		if _, err := tx.Exec(ctx, `INSERT INTO ships_in(tenant_id,item_node_id,project_node_id,rank,source,placed_by) VALUES($1,$2,$3,'V','seed',$4)`, f.tenant, backlog, f.projectA, f.actor); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET kind_id=$2 WHERE id=$1`, backlog, f.kinds["memory"]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE ships_in SET release_node_id=$2,rank='X' WHERE item_node_id=$1`, backlog, f.nextRelease); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "P0001", "live epic, ticket or task", `UPDATE ships_in SET item_node_id=item_node_id WHERE item_node_id=$1`, backlog)
		return nil
	})
}

func TestDeliveryNodeIdentityGuardCatchesCascadeAndPreservesRows(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-g3")
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		deliveryReject(t, ctx, tx, "P0001", "release node cannot move", `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.releaseA, f.projectB)
		deliveryReject(t, ctx, tx, "P0001", "release node cannot move", `UPDATE nodes SET kind_id=$2 WHERE id=$1`, f.releaseA, f.kinds["ticket"])
		deliveryReject(t, ctx, tx, "P0001", "release node cannot move", `UPDATE nodes SET deleted_at=now() WHERE id=$1`, f.releaseA)
		deliveryReject(t, ctx, tx, "P0001", "placed item cannot leave", `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticketA, f.projectB)
		deliveryReject(t, ctx, tx, "P0001", "unshippable kind", `UPDATE nodes SET kind_id=$2 WHERE id=$1`, f.ticketA, f.kinds["memory"])
		// Permitted-kind conversion retains membership.
		if _, err := tx.Exec(ctx, `UPDATE nodes SET kind_id=$2 WHERE id=$1`, f.ticketA, f.kinds["task"]); err != nil {
			return err
		}
		parent := insertNode(ctx, t, tx, f.visibilityFixture, "epic", "PARENT-1", &f.projectA)
		if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.releaseA, parent); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "P0001", "release node cannot move", `UPDATE nodes SET parent_id=$2 WHERE id=$1`, parent, f.projectB)
		var actualParent, actualProject, childProject string
		if err := tx.QueryRow(ctx, `SELECT p.parent_id::text,p.project_id::text,c.project_id::text FROM nodes p JOIN nodes c ON c.tenant_id=p.tenant_id AND c.parent_id=p.id WHERE p.id=$1 AND c.id=$2`, parent, f.releaseA).Scan(&actualParent, &actualProject, &childProject); err != nil {
			return err
		}
		if actualParent != f.projectA || actualProject != f.projectA || childProject != f.projectA {
			t.Fatal("failed cascade partially moved subtree")
		}
		// Project soft deletion is inert; projections stay for restoration.
		leafProject := insertNode(ctx, t, tx, f.visibilityFixture, "project", "LEAF-1", nil)
		if _, err := tx.Exec(ctx, `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by) VALUES($1,$2,$3)`, f.tenant, leafProject, f.actor); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, leafProject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, leafProject); err != nil {
			return err
		}
		var retained int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM project_delivery WHERE project_node_id=$1`, leafProject).Scan(&retained); err != nil {
			return err
		}
		if retained != 1 {
			t.Fatal("project restore lost its planning mode")
		}
		if _, err := tx.Exec(ctx, `UPDATE project_releases SET state='abandoned',abandoned_at=now() WHERE release_node_id=$1`, f.nextRelease); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, f.nextRelease); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, f.nextRelease); err != nil {
			return err
		}
		// Backlog identity is fenced too, until the authorized move removes its row.
		if _, err := tx.Exec(ctx, `UPDATE ships_in SET release_node_id=NULL WHERE item_node_id=$1`, f.ticketA); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "P0001", "placed item cannot leave", `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticketA, f.projectB)
		if _, err := tx.Exec(ctx, `DELETE FROM ships_in WHERE item_node_id=$1`, f.ticketA); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticketA, f.projectB); err != nil {
			return err
		}
		return nil
	})
}

func TestDeliveryPlacementRanksAndExpedite(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-placement-bounds")
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE ships_in SET release_node_id=NULL,expedite=true WHERE item_node_id=$1`, f.ticketA); err != nil {
			return err
		}
		q := `INSERT INTO ships_in(tenant_id,item_node_id,project_node_id,rank,source,placed_by,expedite) VALUES($1,$2,$3,$4,'backfill',$5,$6)`
		// NULL release ids still share one ranked backlog container.
		deliveryReject(t, ctx, tx, "23505", "key", q, f.tenant, f.task, f.projectA, "V", f.actor, false)
		deliveryReject(t, ctx, tx, "23505", "ships_in_expedite_idx", q, f.tenant, f.task, f.projectA, "W", f.actor, true)
		if _, err := tx.Exec(ctx, q, f.tenant, f.task, f.projectA, strings.Repeat("A", 32), f.actor, false); err != nil {
			return err
		}
		for _, rank := range []string{"", "0", "V0", strings.Repeat("A", 33), "a-"} {
			deliveryReject(t, ctx, tx, "23514", "ships_in_rank_check", `UPDATE ships_in SET rank=$2 WHERE item_node_id=$1`, f.task, rank)
		}
		deliveryReject(t, ctx, tx, "23514", "ships_in_revision_check", `UPDATE ships_in SET revision=0 WHERE item_node_id=$1`, f.task)
		return nil
	})
}

func TestDeliveryAdoptionJobJournalEvidenceAndBounds(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-job-bounds")
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		// SQL CHECKs are asserted by name, so a different failure cannot pass.
		for _, tc := range []struct{ set, check string }{
			{"revision=0", "delivery_adoption_jobs_revision_check"},
			{"attempts=-1", "delivery_adoption_jobs_attempts_check"},
			{"reason_message=repeat('é',1025)", "delivery_adoption_jobs_reason_message_check"},
			{"failure_summary=repeat('a',2049)", "delivery_adoption_jobs_failure_summary_check"},
			{"rollout_artifact_ref=repeat('a',513)", "delivery_adoption_jobs_rollout_artifact_ref_check"},
			{"report_ref='report',report_digest='bad'", "delivery_adoption_jobs_report_digest_check"},
			{"lease_generation=-1", "delivery_adoption_jobs_lease_generation_check"},
			{"reserved_backup_bytes=-1,resource_attempt_id=gen_random_uuid()", "delivery_adoption_jobs_reserved_backup_bytes_check"},
			{"reserved_restore_slots=2,resource_attempt_id=gen_random_uuid()", "delivery_adoption_jobs_reserved_restore_slots_check"},
			{"operation_journal='{}'::jsonb", "delivery_adoption_jobs_operation_journal_check"},
			{"operation_journal='[]'::jsonb", "delivery_adoption_jobs_operation_journal_check"},
			{"operation_journal='{\"operations\":[],\"credentials\":\"forbidden\"}'::jsonb", "delivery_adoption_jobs_operation_journal_check"},
			{"operation_journal='{\"operations\":[{}]}'::jsonb", "delivery_adoption_jobs_operation_journal_check"},
			{"operation_journal='{\"operations\":[1]}'::jsonb", "delivery_adoption_jobs_operation_journal_check"},
			{"operation_journal=jsonb_build_object('operations',jsonb_build_array(jsonb_build_object('operation_key',repeat('a',16385),'attempt_id','opaque','kind','backup','status','intent')))", "delivery_adoption_jobs_operation_journal_check"},
		} {
			deliveryReject(t, ctx, tx, "23514", tc.check, `UPDATE delivery_adoption_jobs SET `+tc.set+` WHERE project_node_id=$1`, f.projectA)
		}
		for _, tc := range []struct{ set, check string }{
			{"lease_token=gen_random_uuid(),lease_generation=1", "delivery_adoption_jobs_lease_pair"},
			{"lease_token=gen_random_uuid(),lease_until=now()", "delivery_adoption_jobs_leased_generation"},
			{"state='applying'", "delivery_adoption_jobs_recovery_proof"},
			{"state='adopted',adopted_at=now()", "delivery_adoption_jobs_recovery_proof"},
			{"backup_verified_at=now()", "delivery_adoption_jobs_backup_verification"},
			{"reserved_backup_bytes=1", "delivery_adoption_jobs_resource_attempt"},
			{"attempt_id=gen_random_uuid(),attempt_started_at=now(),attempt_deadline_at=now()+interval '16 minutes'", "delivery_adoption_jobs_attempt_deadline"},
		} {
			deliveryReject(t, ctx, tx, "23514", tc.check, `UPDATE delivery_adoption_jobs SET `+tc.set+` WHERE project_node_id=$1`, f.projectA)
		}
		op := map[string]any{"operation_key": "stable-key", "attempt_id": "old-attempt", "kind": "backup", "status": "intent", "provider_handle": "opaque-handle"}
		journal := func(n int) string {
			ops := make([]any, n)
			for i := range ops {
				ops[i] = op
			}
			b, err := json.Marshal(map[string]any{"operations": ops})
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		if _, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET operation_journal=$2::jsonb,lease_token=gen_random_uuid(),lease_generation=1,lease_until=now()+interval '1 minute',resource_attempt_id=gen_random_uuid(),reserved_backup_bytes=1,reserved_restore_slots=1,next_reconcile_at=now() WHERE project_node_id=$1`, f.projectA, journal(8)); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "23514", "operation_journal_check", `UPDATE delivery_adoption_jobs SET operation_journal=$2::jsonb WHERE project_node_id=$1`, f.projectA, journal(9))
		op["raw_provider_body"] = "forbidden"
		deliveryReject(t, ctx, tx, "23514", "operation_journal_check", `UPDATE delivery_adoption_jobs SET operation_journal=$2::jsonb WHERE project_node_id=$1`, f.projectA, journal(1))
		// Reclaiming a lease preserves the old operation journal/resource charge.
		if _, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET lease_generation=2,lease_token=gen_random_uuid(),cleanup_state='pending' WHERE project_node_id=$1`, f.projectA); err != nil {
			return err
		}
		var n int
		var bytes int64
		if err := tx.QueryRow(ctx, `SELECT jsonb_array_length(operation_journal->'operations'),reserved_backup_bytes FROM delivery_adoption_jobs WHERE project_node_id=$1`, f.projectA).Scan(&n, &bytes); err != nil {
			return err
		}
		if n != 8 || bytes != 1 {
			t.Fatal("lease reclaim erased outstanding resources")
		}
		return nil
	})
}

// The AEON-490 kit is not on this baseline. These five additive entries use
// its exact registry format and must be merged into its complete inventory
// when the coordinator integrates that independent package.
func TestDeliveryDSARInventoryCoversEveryNewColumn(t *testing.T) {
	d := dbtest.Open(t)
	b, err := os.ReadFile("../dsar/inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("delivery inventory", func(t *testing.T) {
		assertDeliveryDSARInventory(t, d, b)
	})
	// The workspace inventory also contains account-scoped data with no project
	// locator. Unioning it must still check every column of all five P1 tables.
	var workspaceEntries []json.RawMessage
	if err := json.Unmarshal(b, &workspaceEntries); err != nil {
		t.Fatal(err)
	}
	workspaceEntries = append(workspaceEntries, json.RawMessage(`{"table":"account_allowance_windows","export":"starts_at ends_at allowance used reserved","metadata":"tenant_id id account_id","locator":"id account_id","hold":"audit-review"}`))
	union, err := json.Marshal(workspaceEntries)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("workspace inventory union", func(t *testing.T) {
		assertDeliveryDSARInventory(t, d, union)
	})
}

func assertDeliveryDSARInventory(t *testing.T, d *dbtest.DB, b []byte) {
	t.Helper()
	var entries []struct{ Table, Export, Review, Secret, Metadata, Subjects, Locator, Hold string }
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatal(err)
	}
	byTable := make(map[string]map[string]string)
	for _, e := range entries {
		if !slices.Contains(deliveryTables, e.Table) {
			continue
		}
		columns := make(map[string]string)
		for class, list := range map[string]string{"personal": e.Export, "review": e.Review, "secret": e.Secret, "metadata": e.Metadata} {
			for _, col := range strings.Fields(list) {
				if columns[col] != "" {
					t.Fatalf("duplicate classification %s.%s", e.Table, col)
				}
				columns[col] = class
			}
		}
		for _, col := range strings.Fields(e.Subjects) {
			if columns[col] != "personal" && columns[col] != "review" {
				t.Fatalf("subject classified as nonpersonal: %s.%s", e.Table, col)
			}
		}
		if !strings.Contains(e.Locator, "project_node_id") {
			t.Fatalf("missing project locator: %s", e.Table)
		}
		byTable[e.Table] = columns
	}
	rows, err := d.App.Query(t.Context(), `SELECT table_name,column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=ANY($1::text[]) ORDER BY table_name,ordinal_position`, deliveryTables)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		if byTable[table][column] == "" {
			t.Fatalf("unclassified column %s.%s", table, column)
		}
		delete(byTable[table], column)
		seen[table] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range deliveryTables {
		if !seen[table] || len(byTable[table]) != 0 {
			t.Fatalf("inventory/schema mismatch %s: %v", table, byTable[table])
		}
	}
}
