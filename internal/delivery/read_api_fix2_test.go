// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestFix2RecoveryPopulationIgnoresCurrentAndEarlierWork(t *testing.T) {
	f := newStoreFixture(t)
	adopted := f.clock.Add(-time.Hour)
	f.exec(t, `UPDATE project_delivery SET adopted_at=$2 WHERE project_node_id=$1`, f.project, adopted)
	earlier := f.addRelease(t, f.project, "REL-3", "internal", "planned", "A")
	// All 5,001 unrelated rows sort ahead of the eligible recovery items.
	// Half are in the current release; half are in an earlier active release.
	f.exec(t, `WITH added AS (
 INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state,updated_at)
 SELECT $1,$2,'TK-'||i,'Unrelated completed work',$3,'done',$4 FROM generate_series(10,5010) i RETURNING id,key)
 INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,release_node_id,rank,source,placed_by)
 SELECT $1,$3,id,CASE WHEN split_part(key,'-',2)::int%2=0 THEN $5::uuid ELSE $6::uuid END,
 lpad(split_part(key,'-',2),6,'0')||'V','person',$7 FROM added`, f.tenant, f.kinds["ticket"], f.project, f.clock, f.release, earlier.ID, f.person.ID)
	if n := f.scalar(t, `SELECT count(*) FROM ships_in WHERE release_node_id=ANY($1::uuid[])`, []string{f.release, earlier.ID}); n != 5001 {
		t.Fatalf("unrelated fixture has %d rows", n)
	}
	tail := f.item(t, "task", "TSK-1", "done", "", "")
	backlog := f.item(t, "ticket", "TK-1", "accepted", "", "V")
	laterA := f.item(t, "task", "TSK-2", "delivered", f.next, "V")
	laterB := f.item(t, "ticket", "TK-2", "done", f.next, "W")
	for i, id := range []string{tail, backlog, laterA, laterB} {
		at := adopted.Add(time.Duration(i+1) * time.Minute)
		f.exec(t, `UPDATE nodes SET updated_at=$2 WHERE id=$1`, id, at)
		f.exec(t, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,before,after,at)
 SELECT $1,$2,id,'node.updated','{"state":"open"}',jsonb_build_object('state',state),$4 FROM nodes WHERE id=$3`, f.tenant, f.person.ID, id, at)
	}
	for _, mode := range []struct {
		name, release string
		options       ReadOptions
		want          []string
	}{
		{"unplaced", "", ReadOptions{CompletedUnplaced: true, Limit: 1}, []string{backlog, tail}},
		{"later", f.release, ReadOptions{CompletedLater: true, Limit: 1}, []string{laterB, laterA}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			opt := mode.options
			for i, id := range mode.want {
				page, err := f.store.Items(t.Context(), f.person, f.project, mode.release, opt)
				if err != nil {
					t.Fatal(err)
				}
				if page.Count != 2 || page.Incomplete || len(page.Items) != 1 || page.Items[0].ItemID != id || (page.NextCursor != "") != (i == 0) {
					t.Fatalf("eligible recovery page %d was crowded out: %+v", i+1, page)
				}
				opt.Cursor = page.NextCursor
			}
		})
	}
	// Warning counts use the same qualifying population, independently of paging.
	r := f.releaseRow(t, f.release)
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		counts, err := recoveryCounts(ctx, tx, f.person, r)
		if err != nil {
			return err
		}
		if counts.Unplaced != 2 || counts.Later != 2 || counts.Incomplete {
			t.Fatalf("warning counts were crowded out: %+v", counts)
		}
		return nil
	})
}
