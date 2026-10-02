// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type attachmentBarrier struct {
	reached, resume chan struct{}
	once            sync.Once
}

func newAttachmentBarrier(t *testing.T) *attachmentBarrier {
	t.Helper()
	b := &attachmentBarrier{reached: make(chan struct{}), resume: make(chan struct{})}
	t.Cleanup(func() { b.release() })
	return b
}
func (b *attachmentBarrier) release() { b.once.Do(func() { close(b.resume) }) }
func (b *attachmentBarrier) wait(ctx context.Context) {
	close(b.reached)
	select {
	case <-b.resume:
	case <-ctx.Done():
	}
}

type pausedAttachmentBody struct {
	io.Reader
	ctx     context.Context
	barrier *attachmentBarrier
	once    sync.Once
}

func (b *pausedAttachmentBody) Read(p []byte) (int, error) {
	b.once.Do(func() { b.barrier.wait(b.ctx) })
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.Reader.Read(p)
}

// DELETE has no body. Pause just after the principal precheck commits using
// pgx's test-pool tracer, without adding a production handler hook.
type attachmentPrecheckTracer struct {
	barrier *attachmentBarrier
	once    sync.Once
}
type attachmentQueryKey struct{}

func (tr *attachmentPrecheckTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, attachmentQueryKey{}, data.SQL)
}
func (tr *attachmentPrecheckTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if query, _ := ctx.Value(attachmentQueryKey{}).(string); strings.EqualFold(query, "commit") && data.Err == nil {
		tr.once.Do(func() { tr.barrier.wait(ctx) })
	}
}

func TestAttachmentMutationRechecksLiveAuthority(t *testing.T) {
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		for _, change := range []string{"revoke", "move", "unchanged"} {
			t.Run(method+"/"+change, func(t *testing.T) {
				d, p, node := setup(t)
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				var source, destination, role, attachment string
				if err := db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
					for i, project := range []*string{&source, &destination} {
						key := []string{"SRC-1", "DST-1"}[i]
						if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,$2,id,$2 FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, p.TenantID, key).Scan(project); err != nil {
							return err
						}
					}
					if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, node, source); err != nil {
						return err
					}
					if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'attachment_writer','Attachment writer') RETURNING id::text`, p.TenantID).Scan(&role); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read'),($1,$2,'attachments.read'),($1,$2,'attachments.write'),($1,$2,'attachments.delete')`, p.TenantID, role); err != nil {
						return err
					}
					// Retain read access in both projects even when write authority changes.
					if _, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='viewer') WHERE tenant_id=$1 AND principal_id=$2`, p.TenantID, p.ID); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, p.TenantID, p.ID, role, source); err != nil {
						return err
					}
					return tx.QueryRow(ctx, `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,created_by) VALUES($1,$2,repeat('ab',32),'fixture.txt','text/plain',7,$3) RETURNING id::text`, p.TenantID, node, p.ID).Scan(&attachment)
				}); err != nil {
					t.Fatal(err)
				}
				barrier := newAttachmentBarrier(t)
				defer barrier.release()
				pool := d.App
				if method == "DELETE" {
					cfg, err := pgxpool.ParseConfig(d.AppURL)
					if err != nil {
						t.Fatal(err)
					}
					cfg.ConnConfig.Tracer = &attachmentPrecheckTracer{barrier: barrier}
					pool, err = pgxpool.NewWithConfig(ctx, cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer pool.Close()
				}
				mux := http.NewServeMux()
				New(pool, Store{FilesDir: t.TempDir(), MaxSize: 1 << 20}).Mount(mux)
				var body io.Reader
				path, ct := "/api/attachments/"+attachment, "application/json"
				if method == "POST" {
					path = "/api/nodes/" + node + "/attachments"
					ct, body = multipartFile(t, "new.txt", []byte("synthetic upload"))
				}
				if method == "PATCH" {
					body = strings.NewReader(`{"caption":"changed"}`)
				}
				if body != nil {
					body = &pausedAttachmentBody{Reader: body, ctx: ctx, barrier: barrier}
				}
				requestCtx := authz.WithRouteScope(tenant.WithPrincipal(ctx, p), authz.Scope{ProjectID: source})
				req := httptest.NewRequest(method, path, body).WithContext(requestCtx)
				req.Header.Set("Content-Type", ct)
				response := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { defer close(done); mux.ServeHTTP(response, req) }()
				select {
				case <-barrier.reached:
				case <-done:
					t.Fatalf("request never reached barrier: %d", response.Code)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				// Body reads/prechecks must not hold the write locks: these changes must
				// commit before releasing the request, or the deadline fails the test.
				if err := db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
					if err := authz.LockProjectMutation(ctx, tx, p.TenantID); err != nil {
						return err
					}
					switch change {
					case "revoke":
						_, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_id=$1 AND permission IN ('attachments.write','attachments.delete')`, role)
						return err
					case "move":
						_, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, node, destination)
						return err
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				barrier.release()
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				want := http.StatusForbidden
				if change == "unchanged" {
					want = map[string]int{"POST": 201, "PATCH": 200, "DELETE": 204}[method]
				}
				if response.Code != want {
					t.Fatalf("status %d, want %d: %s", response.Code, want, response.Body.String())
				}
				var count, changed, eventCount int
				if err := d.Admin.QueryRow(ctx, `SELECT
     (SELECT count(*) FROM attachments WHERE node_id=$1),
     (SELECT count(*) FROM attachments WHERE node_id=$1 AND (caption<>'' OR deleted_at IS NOT NULL)),
     (SELECT count(*) FROM events WHERE type LIKE 'attachment.%')`, node).Scan(&count, &changed, &eventCount); err != nil {
					t.Fatal(err)
				}
				if change != "unchanged" && (count != 1 || changed != 0 || eventCount != 0) {
					t.Fatalf("denied write persisted metadata/events: %d %d %d", count, changed, eventCount)
				}
				if change == "unchanged" && eventCount != 1 {
					t.Fatalf("allowed write event count: %d", eventCount)
				}
			})
		}
	}
}

func TestAttachmentWriteHoldsAuthorityUntilCommit(t *testing.T) {
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			d, p, node := setup(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			mux := http.NewServeMux()
			New(d.App, Store{FilesDir: t.TempDir(), MaxSize: 1 << 20}).Mount(mux)
			ct, body := multipartFile(t, "fixture.txt", []byte("synthetic"))
			uploaded := request(t, mux, p, "POST", "/api/nodes/"+node+"/attachments", ct, body)
			if uploaded.Code != 201 {
				t.Fatalf("fixture upload: %d", uploaded.Code)
			}
			var attachment string
			if err := d.Admin.QueryRow(ctx, `SELECT id::text FROM attachments WHERE node_id=$1`, node).Scan(&attachment); err != nil {
				t.Fatal(err)
			}
			blocker, err := d.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			query, id := `SELECT id FROM attachments WHERE id=$1 FOR UPDATE`, attachment
			if method == "POST" {
				query, id = `SELECT id FROM nodes WHERE id=$1 FOR UPDATE`, node
			}
			if _, err := blocker.Exec(ctx, query, id); err != nil {
				t.Fatal(err)
			}
			path, contentType := "/api/attachments/"+attachment, "application/json"
			var input io.Reader
			if method == "POST" {
				path = "/api/nodes/" + node + "/attachments"
				contentType, input = multipartFile(t, "second.txt", []byte("second synthetic"))
			}
			if method == "PATCH" {
				input = strings.NewReader(`{"caption":"allowed"}`)
			}
			req := httptest.NewRequest(method, path, input).WithContext(tenant.WithPrincipal(ctx, p))
			req.Header.Set("Content-Type", contentType)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); mux.ServeHTTP(response, req) }()
			for {
				select {
				case <-done:
					t.Fatalf("request completed before row barrier: %d", response.Code)
				default:
				}
				var waiting bool
				if err := d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE NOT granted AND $1::int=ANY(pg_blocking_pids(pid)))`, blocker.Conn().PgConn().PID()).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				runtime.Gosched()
			}
			// Resource mutation is paused. Authority changes must remain fenced for
			// the rest of this transaction, even after the live permission check.
			probe, err := d.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE NOWAIT`, p.TenantID)
			_ = probe.Rollback(ctx)
			var pe *pgconn.PgError
			if !errors.As(err, &pe) || pe.Code != "55P03" {
				t.Errorf("attachment write did not fence access changes: %v", err)
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if want := map[string]int{"POST": 201, "PATCH": 200, "DELETE": 204}[method]; response.Code != want {
				t.Fatalf("write after release: %d, want %d", response.Code, want)
			}
		})
	}
}
