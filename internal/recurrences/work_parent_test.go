// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"os"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestRecurrenceCreatesWorkLeafAndReopensDoneParent(t *testing.T) {
	f := setup(t)
	dbtest.EnableWorkParentStatus(t, f.d, f.p.TenantID)
	in := f.input()
	in.Template.Type = "task"
	r := f.create(in)
	if r.Template.Type != "work" {
		t.Fatalf("legacy template not normalized: %q", r.Template.Type)
	}
	first := f.manual(r.ID, "first")
	if first.Outcome != "created" || first.NodeID == nil {
		t.Fatalf("first occurrence: %+v", first)
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"hide_from_release_notes":true}'::jsonb,state='done' WHERE id=$1`, *first.NodeID)
		return err
	})
	assertParent := func(want string) {
		t.Helper()
		f.tx(func(tx pgx.Tx) error {
			var state string
			err := tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=$1`, f.parent).Scan(&state)
			if state != want {
				t.Errorf("parent state %s, want %s", state, want)
			}
			return err
		})
	}
	assertParent("done")
	// Parent Done is not an event trigger and the injected clock is before the
	// next due slot. No sleep or elapsed-time assertion establishes this claim.
	if err := f.m.RunTenant(t.Context(), f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if got := f.get(r.ID).OccurrenceCount; got != 1 {
		t.Fatalf("parent completion recurred: %d", got)
	}
	second := f.manual(r.ID, "second")
	if second.Outcome != "created" || second.NodeID == nil || *second.NodeID == *first.NodeID {
		t.Fatalf("second occurrence: %+v", second)
	}
	assertParent("open")
	f.tx(func(tx pgx.Tx) error {
		var slug string
		var leaf bool
		err := tx.QueryRow(t.Context(), `SELECT k.slug,NOT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds ck ON ck.id=c.kind_id AND ck.tenant_id=c.tenant_id WHERE c.parent_id=n.id AND c.deleted_at IS NULL AND ck.slug='work') FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1`, *second.NodeID).Scan(&slug, &leaf)
		if slug != "work" || !leaf {
			t.Errorf("occurrence kind=%s leaf=%v", slug, leaf)
		}
		return err
	})
}

func TestStoredRecurrenceKindMigrationPreservesReceipts(t *testing.T) {
	f := setup(t)
	r := f.create(f.input())
	first := f.manual(r.ID, "historical")
	sql, err := os.ReadFile("../db/migrations/1234_recurrence_work_templates.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"epic", "ticket", "task"} {
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE recurrences SET template=jsonb_set(template,'{type}',to_jsonb($2::text)) WHERE id=$1`, r.ID, kind)
			return err
		})
		prior := f.get(r.ID)
		f.tx(func(tx pgx.Tx) error { _, err := tx.Exec(t.Context(), string(sql)); return err })
		after := f.get(r.ID)
		if after.Template.Type != "work" || after.Template.Title != prior.Template.Title || after.Revision != prior.Revision+1 || after.OccurrenceCount != prior.OccurrenceCount {
			t.Fatalf("%s migration changed definition: before=%+v after=%+v", kind, prior, after)
		}
		replay := f.manual(r.ID, "historical")
		if replay.NodeID == nil || first.NodeID == nil || *replay.NodeID != *first.NodeID || replay.Key != first.Key || replay.Number != first.Number {
			t.Fatalf("historical receipt changed: %+v", replay)
		}
	}
}

func TestStoredLegacyTemplateCreatesQueuedWorkLeafAfterLockedReload(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.QueueEach = true
	r := f.create(in)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE recurrences SET template=jsonb_set(template,'{type}','"task"'::jsonb) WHERE id=$1`, r.ID)
		return err
	})
	occurrence := f.manual(r.ID, "legacy-queued")
	if occurrence.Outcome != "created" || occurrence.NodeID == nil {
		t.Fatalf("legacy definition failed: %+v", occurrence)
	}
	f.tx(func(tx pgx.Tx) error {
		var kind string
		var queued int
		err := tx.QueryRow(t.Context(), `SELECT k.slug,(SELECT count(*) FROM agent_runs WHERE queue_node_id=n.id) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1`, *occurrence.NodeID).Scan(&kind, &queued)
		if kind != "work" || queued != 1 {
			t.Errorf("kind=%s queued=%d", kind, queued)
		}
		return err
	})
}
