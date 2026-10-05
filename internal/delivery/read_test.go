// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

type readFixture struct {
	d                                     *dbtest.DB
	tenant, actor, project, other, legacy string
	release, next                         string
	kinds                                 map[string]string
}

func newReadFixture(t *testing.T) *readFixture {
	t.Helper()
	f := &readFixture{d: dbtest.Open(t), kinds: map[string]string{}}
	ctx := dbtest.Seed(t.Context())
	if err := f.d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('delivery-read','delivery-read') RETURNING id::text`).Scan(&f.tenant); err != nil {
		t.Fatal(err)
	}
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT slug,id::text FROM node_kinds`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k, id string
			if err := rows.Scan(&k, &id); err != nil {
				rows.Close()
				return err
			}
			f.kinds[k] = id
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Reader') RETURNING id::text`, f.tenant).Scan(&f.actor); err != nil {
			return err
		}
		f.project = f.node(t, ctx, tx, "project", "PR-1", "")
		f.other = f.node(t, ctx, tx, "project", "PR-2", "")
		f.legacy = f.node(t, ctx, tx, "project", "PR-3", "")
		for _, project := range []string{f.project, f.other} {
			if _, err := tx.Exec(ctx, `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by,next_sequence) VALUES($1,$2,$3,3)`, f.tenant, project, f.actor); err != nil {
				return err
			}
		}
		f.release = f.node(t, ctx, tx, "release", "REL-1", f.project)
		f.next = f.node(t, ctx, tx, "release", "REL-2", f.project)
		for i, id := range []string{f.release, f.next} {
			if _, err := tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank) VALUES($1,$2,$3,$4,$5)`, f.tenant, f.project, id, i+1, []string{"B", "D"}[i]); err != nil {
				return err
			}
		}
		return nil
	})
	return f
}

func (f *readFixture) run(t *testing.T, fn func(context.Context, pgx.Tx) error) {
	t.Helper()
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, f.d.App, f.tenant, func(tx pgx.Tx) error { return fn(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
}

func (f *readFixture) node(t *testing.T, ctx context.Context, tx pgx.Tx, kind, key, project string) string {
	t.Helper()
	var id string
	var parent any
	if project != "" {
		parent = project
	}
	if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) VALUES($1,$2,$3,$3,$4) RETURNING id::text`, f.tenant, f.kinds[kind], key, parent).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *readFixture) place(t *testing.T, ctx context.Context, tx pgx.Tx, item, release, rank string) {
	t.Helper()
	var container any
	if release != "" {
		container = release
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,release_node_id,rank,source,placed_by) VALUES($1,$2,$3,$4,$5,'person',$6)`, f.tenant, f.project, item, container, rank, f.actor); err != nil {
		t.Fatal(err)
	}
}

func TestEffectivePlacement(t *testing.T) {
	f := newReadFixture(t)
	var tail, backlog, placed, completed, epic, deleted, hidden string
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		tail = f.node(t, ctx, tx, "ticket", "TK-1", f.project)
		backlog = f.node(t, ctx, tx, "task", "TSK-1", f.project)
		f.place(t, ctx, tx, backlog, "", "V")
		placed = f.node(t, ctx, tx, "ticket", "TK-2", f.project)
		f.place(t, ctx, tx, placed, f.release, "V")
		completed = f.node(t, ctx, tx, "task", "TSK-2", f.project)
		f.place(t, ctx, tx, completed, f.release, "W")
		epic = f.node(t, ctx, tx, "epic", "EP-1", f.project)
		f.place(t, ctx, tx, epic, f.release, "X")
		deleted = f.node(t, ctx, tx, "ticket", "TK-3", f.project)
		f.place(t, ctx, tx, deleted, f.release, "Y")
		hidden = f.node(t, ctx, tx, "ticket", "TK-4", f.project)
		f.place(t, ctx, tx, hidden, "", "W")
		f.node(t, ctx, tx, "ticket", "OLD-1", f.legacy)
		f.node(t, ctx, tx, "release", "REL-3", f.project)
		if _, err := tx.Exec(ctx, `UPDATE ships_in SET expedite=true WHERE item_node_id=$1`, completed); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET state='done',updated_at=clock_timestamp() WHERE id=$1`, completed); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, deleted); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE nodes SET kind_id=$2 WHERE id=$1`, hidden, f.kinds["release"])
		return err
	})
	query := `SELECT item_node_id::text,release_node_id::text,rank,revision,expedite FROM ` + Effective + ` AS e WHERE tenant_id=$1 AND project_node_id=$2`
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, f.tenant, f.project)
		if err != nil {
			return err
		}
		defer rows.Close()
		found := map[string]bool{}
		for rows.Next() {
			var id string
			var release, rank *string
			var revision int64
			var expedite bool
			if err := rows.Scan(&id, &release, &rank, &revision, &expedite); err != nil {
				return err
			}
			found[id] = true
			switch id {
			case tail:
				if release != nil || rank != nil || revision != 0 {
					t.Fatal("new create did not land in the unranked tail")
				}
			case backlog:
				if release != nil || rank == nil || *rank != "V" || revision != 1 {
					t.Fatal("ranked backlog lost placement")
				}
			case placed, completed, epic:
				if release == nil || *release != f.release || rank == nil || revision != 1 {
					t.Fatal("release placement lost")
				}
			default:
				t.Fatalf("unexpected effective item %s", id)
			}
			if expedite {
				t.Fatal("completed unit retained effective expedite")
			}
		}
		if len(found) != 5 {
			t.Fatalf("effective set has %d items, want 5", len(found))
		}
		// Hidden/tombstone data stays present; reads must not destroy it.
		var retained int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ships_in WHERE item_node_id=ANY($1::uuid[])`, []string{deleted, hidden}).Scan(&retained); err != nil {
			return err
		}
		if retained != 2 {
			t.Fatal("read lost hidden placements")
		}
		return rows.Err()
	})
	// Delete children first, as the node tree guard requires. Release-node
	// tombstones also require abandoned lifecycle; these are raw read fixtures,
	// not a claim that a store abandon has run. All stored placements survive.
	var liveChildren []string
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM nodes WHERE parent_id=$1 AND deleted_at IS NULL`, f.project)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			liveChildren = append(liveChildren, id)
		}
		rows.Close()
		return rows.Err()
	})
	for _, deletedProject := range []bool{true, false} {
		f.run(t, func(ctx context.Context, tx pgx.Tx) error {
			if deletedProject {
				if _, err := tx.Exec(ctx, `UPDATE project_releases SET state='abandoned',abandoned_at=clock_timestamp() WHERE project_node_id=$1`, f.project); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=ANY($1::uuid[])`, liveChildren); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=CASE WHEN $2 THEN clock_timestamp() END WHERE id=$1`, f.project, deletedProject); err != nil {
				return err
			}
			if !deletedProject {
				if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=NULL WHERE id=ANY($1::uuid[])`, liveChildren); err != nil {
					return err
				}
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+Effective+` e WHERE tenant_id=$1 AND project_node_id=$2`, f.tenant, f.project).Scan(&count); err != nil {
				return err
			}
			want := 5
			if deletedProject {
				want = 0
			}
			if count != want {
				t.Fatalf("deleted=%v: effective count %d, want %d", deletedProject, count, want)
			}
			return nil
		})
	}
	if err := db.InTenant(db.OnlyProjects(t.Context(), f.other), f.d.App, f.tenant, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM `+Effective+` e WHERE tenant_id=$1 AND project_node_id=$2`, f.tenant, f.project).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("effective fragment escaped project RLS")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNeighboursBoundedIndexSeeks(t *testing.T) {
	f := newReadFixture(t)
	keys, err := SeedRanks(128)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(keys))
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		for i, key := range keys {
			ids[i] = f.node(t, ctx, tx, "ticket", fmt.Sprintf("TK-%d", i+1), f.project)
			f.place(t, ctx, tx, ids[i], "", key)
		}
		// A deleted row still owns its key and participates in neighbour seeks.
		_, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, ids[81])
		return err
	})
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		c := Container{TenantID: f.tenant, ProjectID: f.project}
		got, err := ItemNeighbours(ctx, tx, c, keys[80], ids[79])
		if err != nil {
			return err
		}
		if got.Previous == nil || got.Previous.ID != ids[78] || got.Next == nil || got.Next.ID != ids[81] {
			t.Fatalf("wrong neighbours: %+v", got)
		}
		empty, err := ItemNeighbours(ctx, tx, Container{f.tenant, f.other, ""}, "", "")
		if err != nil {
			return err
		}
		if empty.Previous != nil || empty.Next != nil {
			t.Fatal("cross-project neighbours returned")
		}
		for _, previous := range []bool{true, false} {
			q, args := neighbourQuery(c, keys[80], ids[79], true, previous)
			assertNeighbourPlan(t, ctx, tx, q, args)
		}
		for i, rank := range []string{"A", "B", "C"} {
			if _, err := tx.Exec(ctx, `UPDATE ships_in SET release_node_id=$2,rank=$3 WHERE item_node_id=$1`, ids[80+i], f.release, rank); err != nil {
				return err
			}
		}
		c.ReleaseID = f.release
		got, err = ItemNeighbours(ctx, tx, c, "B", "")
		if err != nil {
			return err
		}
		if got.Previous == nil || got.Previous.ID != ids[80] || got.Next == nil || got.Next.ID != ids[82] {
			t.Fatal("wrong release neighbours")
		}
		for _, previous := range []bool{true, false} {
			q, args := neighbourQuery(c, "B", "", true, previous)
			assertNeighbourPlan(t, ctx, tx, q, args)
		}
		return nil
	})
}

type planNode struct {
	NodeType         string     `json:"Node Type"`
	Relation         string     `json:"Relation Name"`
	IndexCond        string     `json:"Index Cond"`
	ActualRows       float64    `json:"Actual Rows"`
	ActualLoops      float64    `json:"Actual Loops"`
	RemovedByFilter  float64    `json:"Rows Removed by Filter"`
	RemovedByRecheck float64    `json:"Rows Removed by Index Recheck"`
	Plans            []planNode `json:"Plans"`
}

func assertNeighbourPlan(t *testing.T, ctx context.Context, tx pgx.Tx, q string, args []any) {
	t.Helper()
	var raw []byte
	if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+q, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan planNode }
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Plan.NodeType != "Limit" || plans[0].Plan.ActualRows != 1 {
		t.Fatal("populated neighbour sample did not return exactly one row")
	}
	found := false
	var visit func(planNode)
	visit = func(p planNode) {
		if p.Relation == "ships_in" {
			if !strings.Contains(p.NodeType, "Index") || !strings.Contains(p.IndexCond, "rank") || p.ActualLoops != 1 || p.ActualRows+p.RemovedByFilter+p.RemovedByRecheck > 2 {
				t.Fatalf("unbounded neighbour scan: %+v; plan %s", p, raw)
			}
			found = true
		}
		for _, child := range p.Plans {
			visit(child)
		}
	}
	visit(plans[0].Plan)
	if !found {
		t.Fatal("no ships_in index seek in plan")
	}
}

func TestPublishedBoundsRetainTerminalOrder(t *testing.T) {
	f := newReadFixture(t)
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE project_releases SET state='abandoned',abandoned_at=clock_timestamp() WHERE release_node_id=$1`, f.release); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, f.release); err != nil {
			return err
		}
		lo, hi, err := PublishedBounds(ctx, tx, f.tenant, f.project, 2)
		if err != nil {
			return err
		}
		if lo == nil || lo.Sequence != 1 || lo.Rank != "B" || hi != nil {
			t.Fatalf("terminal published order lost: %+v / %+v", lo, hi)
		}
		if err := CheckPublishedRank(2, "A", lo, hi); !errors.Is(err, ErrPublishedOrder) {
			t.Fatalf("wrong refusal: %v", err)
		}
		got, err := ReleaseNeighbours(ctx, tx, f.tenant, f.project, "C", "")
		if err != nil {
			return err
		}
		if got.Previous == nil || got.Previous.ID != f.release || got.Next == nil || got.Next.ID != f.next {
			t.Fatal("release neighbours lost numbered order")
		}
		return nil
	})
}

func TestInvalidNeighbourInputsRefuseBeforeQuery(t *testing.T) {
	// A nil transaction proves malformed identities/keys never reach SQL.
	if _, err := ItemNeighbours(t.Context(), nil, Container{TenantID: strings.Repeat("x", 100000)}, "", ""); err == nil {
		t.Fatal("unbounded identity accepted")
	}
	if _, _, err := PublishedBounds(t.Context(), nil, "bad", "bad", 1); err == nil {
		t.Fatal("invalid identity accepted")
	}
	valid := "11111111-1111-1111-1111-111111111111"
	if _, err := ItemNeighbours(t.Context(), nil, Container{TenantID: valid, ProjectID: valid}, "A0", ""); !errors.Is(err, ErrInvalidRank) {
		t.Fatalf("wrong rank refusal: %v", err)
	}
}

func TestDatabaseRankCheckRejectsInvalidKeys(t *testing.T) {
	f := newReadFixture(t)
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		item := f.node(t, ctx, tx, "ticket", "TK-1", f.project)
		f.place(t, ctx, tx, item, "", "V")
		for _, rank := range []string{"A0", strings.Repeat("A", 33)} {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			_, err = sp.Exec(ctx, `UPDATE ships_in SET rank=$2 WHERE item_node_id=$1`, item, rank)
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "ships_in_rank_check" {
				t.Fatalf("wrong rejection for %q: %v", rank, err)
			}
			if err := sp.Rollback(ctx); err != nil {
				return err
			}
		}
		var rank string
		if err := tx.QueryRow(ctx, `SELECT rank FROM ships_in WHERE item_node_id=$1`, item).Scan(&rank); err != nil {
			return err
		}
		if rank != "V" {
			t.Fatal("failed writes changed placement")
		}
		return nil
	})
}
