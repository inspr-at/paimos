// SPDX-License-Identifier: AGPL-3.0-only
package activity

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"strings"
	"testing"
	"time"
)

type timelineWork struct {
	candidateRows int64
	reads         int
}

func (w *timelineWork) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, timelineQueryKey{}, q.SQL)
}

type timelineQueryKey struct{}

func (w *timelineWork) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(timelineQueryKey{}).(string)
	if strings.Contains(sql, "FROM events") && (strings.Contains(sql, "e.before") || strings.Contains(sql, "before,after,metadata")) {
		w.reads++
		w.candidateRows += q.CommandTag.RowsAffected()
	}
}
func TestLongTicketHistoryUsesBoundedSuccessivePages(t *testing.T) {
	f := setup(t)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO events(tenant_id,node_id,actor_principal_id,type,at,after)
   SELECT $1,$2,$3,'comment.created','2026-09-01Z'::timestamptz+g*interval '1 second',jsonb_build_object('body_markdown','comment '||g) FROM generate_series(1,2000) g`, f.p.TenantID, f.node, f.p.ID)
		return err
	})
	work := &timelineWork{}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = work
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	mux := http.NewServeMux()
	New(pool).Mount(mux)
	f.h = mux
	first := f.page("?limit=20")
	if len(first.Items) != 20 || first.NextCursor == nil {
		t.Fatal("missing first page")
	}
	latest := first.Items[0].ID
	f.event("comment.updated", time.Now(), nil, record{"comment_id": latest, "body_markdown": "after watermark"})
	second := f.page("?limit=20&cursor=" + *first.NextCursor)
	if len(second.Items) != 20 {
		t.Fatal("missing second page")
	}
	seen := map[string]bool{}
	for _, item := range first.Items {
		seen[item.ID] = true
	}
	for _, item := range second.Items {
		if seen[item.ID] {
			t.Fatal("successive page repeated an item")
		}
	}
	if work.reads != 2 || work.candidateRows > 130 {
		t.Fatalf("pages read entire same-ticket history: %+v", work)
	}
	// The same cursor fences later backdated imports as well as edits.
	decoded, err := decodeCursor(*first.NextCursor, f.p.TenantID, f.node)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Watermark < 1 {
		t.Fatal("no history watermark")
	}
	if _, err := json.Marshal(second); err != nil {
		t.Fatal(err)
	}
}
