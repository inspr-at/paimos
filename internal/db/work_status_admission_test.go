// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbrand"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type admissionBody struct {
	io.Reader
	ctx              context.Context
	entered, release chan struct{}
}

func (b *admissionBody) Read(p []byte) (int, error) {
	if b.entered != nil {
		close(b.entered)
		b.entered = nil
		select {
		case <-b.release:
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	return b.Reader.Read(p)
}
func (*admissionBody) Close() error { return nil }

type admissionRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *admissionRecorder) SetReadDeadline(at time.Time) error {
	w.deadlines = append(w.deadlines, at)
	return nil
}

func TestWorkStatusEndpointBodiesPrecedeAdmission(t *testing.T) {
	for _, name := range []string{"shared endpoint", "queue", "brand"} {
		t.Run(name, func(t *testing.T) {
			f := newStatusFixture(t)
			p := tenant.Principal{TenantID: f.tid, Kind: tenant.Person}
			f.exec(`INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Owner',ARRAY['super_admin'])`, f.tid)
			if err := f.tx(func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT id::text FROM principals WHERE name='Owner'`).Scan(&p.ID)
			}); err != nil {
				t.Fatal(err)
			}
			dbtest.BindRole(t, f.d, f.tid, p.ID, "owner")
			f.enable()
			mux := http.NewServeMux()
			method, path, body, want := "POST", "/probe", `{"ok":true}`, 200
			switch name {
			case "shared endpoint":
				mux.HandleFunc("POST /probe", workorders.Endpoint(f.d.App, "", false, 200, func(r *http.Request, _ pgx.Tx, _ tenant.Principal) (any, error) {
					var in struct {
						OK bool `json:"ok"`
					}
					if err := workorders.Decode(r, &in); err != nil {
						return nil, err
					}
					return in, nil
				}))
			case "queue":
				agentruns.New(f.d.App).Mount(mux)
				path, body, want = "/api/queue", `{}`, 400
			case "brand":
				tenantbrand.New(f.d.App).Mount(mux)
				method, path, body = "PUT", "/api/settings/brand", `{"short_name":"Test"}`
			}
			ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), p), 15*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			req := httptest.NewRequest(method, path, nil).WithContext(ctx)
			req.Body = &admissionBody{Reader: strings.NewReader(body), ctx: ctx, entered: entered, release: release}
			rec := &admissionRecorder{ResponseRecorder: httptest.NewRecorder()}
			done := make(chan struct{})
			go func() { defer close(done); mux.ServeHTTP(rec, req) }()
			select {
			case <-entered:
			case <-done:
				t.Fatalf("body never reached barrier: %d %s", rec.Code, rec.Body.String())
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			probe, err := f.d.Admin.Begin(ctx)
			if err != nil {
				close(release)
				<-done
				t.Fatal(err)
			}
			var free bool
			err = probe.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, f.tid).Scan(&free)
			if err == nil && free {
				_, err = probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, f.tid)
			}
			_ = probe.Rollback(ctx)
			close(release)
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err != nil || !free {
				t.Fatalf("stalled endpoint body holds tenant locks: free=%v err=%v", free, err)
			}
			if rec.Code != want {
				t.Fatalf("response %d %s, want %d", rec.Code, rec.Body.String(), want)
			}
			if len(rec.deadlines) != 2 || rec.deadlines[0].IsZero() || !rec.deadlines[1].IsZero() {
				t.Fatalf("body read deadline not installed and cleared: %v", rec.deadlines)
			}
		})
	}
}

func TestWorkStatusLastChildKindConversionRetainsAudit(t *testing.T) {
	f := newStatusFixture(t)
	f.node("WK-1", "PRJ-1", "open")
	f.node("WK-2", "WK-1", "open")
	f.enable()
	f.change("WK-2", "in_progress")
	f.check("WK-1", "in_progress")
	var cause int64
	if err := f.tx(func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(t.Context(), `UPDATE nodes SET kind_id=(SELECT id FROM node_kinds WHERE slug='memory') WHERE key='WK-2' RETURNING id::text`).Scan(&id); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after)
		 SELECT $1,id,$2::uuid,'node.kind_changed',jsonb_build_object('id',$2::uuid::text) FROM principals WHERE name='System' RETURNING id`, f.tid, id).Scan(&cause)
	}); err != nil {
		t.Fatal(err)
	}
	f.check("WK-1", "in_progress")
	var retained, derived int
	var recordedCause int64
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='status_autopilot.retained' AND metadata->>'rule_version'='1' AND metadata->>'reason' IS NOT NULL), count(*) FILTER(WHERE type='status_autopilot.derived'), coalesce(max((metadata->>'cause_event_id')::bigint) FILTER(WHERE type='status_autopilot.retained'),0) FROM events WHERE node_id=(SELECT id FROM nodes WHERE key='WK-1')`).Scan(&retained, &derived, &recordedCause)
	}); err != nil {
		t.Fatal(err)
	}
	if retained != 1 || derived != 1 || recordedCause != cause {
		t.Fatalf("kind conversion audit: retained=%d derived=%d cause=%d, want 1 each and cause %d", retained, derived, recordedCause, cause)
	}
	// A later non-membership edit must not emit another retention fact.
	f.exec(`UPDATE nodes SET title='Memory edit' WHERE key='WK-2'`)
	// A tombstone already lost live membership; changing its kind does not
	// remove another work child and must not duplicate the retention fact.
	f.exec(`UPDATE nodes SET deleted_at=clock_timestamp() WHERE key='WK-2'`)
	f.exec(`UPDATE nodes SET kind_id=(SELECT id FROM node_kinds WHERE slug='work') WHERE key='WK-2'`)
	f.exec(`UPDATE nodes SET kind_id=(SELECT id FROM node_kinds WHERE slug='memory') WHERE key='WK-2'`)
	if err := f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='status_autopilot.retained'`).Scan(&retained)
	}); err != nil {
		t.Fatal(err)
	}
	if retained != 1 {
		t.Fatalf("unrelated edit duplicated retention: %d", retained)
	}
}
