// SPDX-License-Identifier: AGPL-3.0-only
package relations

import (
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestCycleSearchBudgetFailsClosed(t *testing.T) {
	for _, wide := range []bool{false, true} {
		t.Run(fmt.Sprint("wide=", wide), func(t *testing.T) {
			f := setup(t)
			n := maxLoopDepth + 2
			if wide {
				n = maxLoopVisited + 1
			}
			var ids []string
			err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
				rows, err := tx.Query(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title)
				 SELECT $1,'BUD-'||i,k.id,'Budget node' FROM generate_series(1,$2::int) i
				 CROSS JOIN node_kinds k WHERE k.tenant_id=$1 AND k.slug='task' RETURNING id::text`, f.a.TenantID, n)
				if err != nil {
					return err
				}
				for rows.Next() {
					var id string
					if err := rows.Scan(&id); err != nil {
						rows.Close()
						return err
					}
					ids = append(ids, id)
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					return err
				}
				for _, typ := range []string{"blocks", "implements", "duplicates"} {
					if wide {
						_, err = tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type)
						 SELECT $1,$2,id,$3 FROM unnest($4::uuid[]) id`, f.a.TenantID, f.nodes[0], typ, ids)
						if err == nil {
							_, err = tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,$4)`, f.a.TenantID, ids[len(ids)-1], f.nodes[1], typ)
						}
					} else {
						_, err = tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type)
						 SELECT $1,a.id,b.id,$2 FROM unnest($3::uuid[]) WITH ORDINALITY a(id,pos)
						 JOIN unnest($3::uuid[]) WITH ORDINALITY b(id,pos) ON b.pos=a.pos+1`, f.a.TenantID, typ, ids)
					}
					if err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			source, target := ids[len(ids)-1], ids[0]
			if wide {
				source, target = f.nodes[1], f.nodes[0]
			}
			for _, typ := range []string{"blocks", "implements", "duplicates"} {
				w := post(f, source, target, typ)
				expect(t, w, 409)
				if !strings.Contains(w.Body.String(), "search limit") {
					t.Fatalf("unactionable refusal: %s", w.Body.String())
				}
			}
			if len(logEvents(t, f)) != 0 {
				t.Fatal("refused search wrote an event")
			}
		})
	}
}
