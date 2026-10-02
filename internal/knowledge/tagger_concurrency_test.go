// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Hold the real tagger after its node row lock. The real autopilot must wait
// for the tree lock, rather than hold tree while waiting for the tagger's row.
type taggerRowPause struct {
	first  atomic.Bool
	locked chan uint32
	resume chan struct{}
}

type taggerRowPauseKey struct{}

func (p *taggerRowPause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL FOR UPDATE") && p.first.CompareAndSwap(false, true) {
		return context.WithValue(ctx, taggerRowPauseKey{}, true)
	}
	return ctx
}

func (p *taggerRowPause) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(taggerRowPauseKey{}) == true && q.Err == nil {
		p.locked <- conn.PgConn().PID()
		select {
		case <-p.resume:
		case <-ctx.Done():
		}
	}
}

func TestTaggerAndStatusAutopilotDoNotDeadlock(t *testing.T) {
	f := setup(t)
	ticket := addNode(t, f, "DONE-534", "ticket", "Concurrent startup jobs", &f.project)
	setFields(t, f, ticket, map[string]any{"priority": "high", "tags": []any{"ops"}})
	closeAged(t, f, ticket, "done", "1 hour")
	pause := &taggerRowPause{locked: make(chan uint32, 1), resume: make(chan struct{})}
	cfg := f.db.App.Config()
	cfg.ConnConfig.Tracer = pause
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	defer func() {
		select {
		case <-pause.resume:
		default:
			close(pause.resume)
		}
	}()
	type result struct {
		n   int
		err error
	}
	tagDone := make(chan result, 1)
	go func() {
		n, err := TagOnce(ctx, pool)
		tagDone <- result{n, err}
	}()
	var pid uint32
	select {
	case pid = <-pause.locked:
	case r := <-tagDone:
		t.Fatalf("tagger ended before row lock: %+v", r)
	case <-ctx.Done():
		t.Fatal("tagger did not acquire its row lock")
	}
	autopilotDone := make(chan error, 1)
	go func() {
		autopilotDone <- statusautopilot.New(f.db.App).RunTenant(ctx, f.a.TenantID, time.Now().Add(20*24*time.Hour))
	}()
	for {
		var waiting, onTree bool
		err := f.db.Admin.QueryRow(ctx, `SELECT
		 EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid))),
		 EXISTS(SELECT 1 FROM pg_locks w JOIN pg_locks h USING(locktype,database,classid,objid,objsubid)
		 WHERE h.pid=$1 AND h.granted AND NOT w.granted AND h.locktype='advisory'
		 AND h.classid=((hashtextextended($2,0)>>32)&4294967295)::oid
		 AND h.objid=(hashtextextended($2,0)&4294967295)::oid)`, pid, f.a.TenantID).Scan(&waiting, &onTree)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			if !onTree {
				t.Fatal("autopilot is blocked by a row instead of waiting for the tagger's tree lock")
			}
			break
		}
		select {
		case err := <-autopilotDone:
			t.Fatalf("autopilot ended before overlapping tagger: %v", err)
		case <-ctx.Done():
			t.Fatal("autopilot did not wait for tagger")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(pause.resume)
	select {
	case r := <-tagDone:
		if r.err != nil || r.n != 1 {
			t.Fatalf("tagger nominated %d: %v", r.n, r.err)
		}
	case <-ctx.Done():
		t.Fatal("tagger deadlocked")
	}
	select {
	case err := <-autopilotDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("autopilot deadlocked")
	}
	var tagged, marked, fieldsKept bool
	var tagEvents, statusEvents int
	err = f.db.Admin.QueryRow(ctx, `SELECT fields->'tags' ? 'process-learning',
	 status_autopilot->>'missed_release'='true', fields->>'priority'='high' AND fields->'tags' ? 'ops',
	 (SELECT count(*) FROM events WHERE tenant_id=$1 AND node_id=$2 AND metadata->>'job'='learning-tagger'),
	 (SELECT count(*) FROM events WHERE tenant_id=$1 AND node_id=$2 AND type=$3)
	 FROM nodes WHERE tenant_id=$1 AND id=$2`, f.a.TenantID, ticket, statusautopilot.Changed).Scan(&tagged, &marked, &fieldsKept, &tagEvents, &statusEvents)
	if err != nil || !tagged || !marked || !fieldsKept || tagEvents != 1 || statusEvents != 1 {
		t.Fatalf("tagged=%v marked=%v fieldsKept=%v events=%d/%d: %v", tagged, marked, fieldsKept, tagEvents, statusEvents, err)
	}
	if _, _, ok := nominationOfKey(t, f, nodeLearningID(ticket)); !ok {
		t.Fatal("concurrent pass lost the nomination")
	}
}

func TestTaggerRetriesRolledBackTenantPass(t *testing.T) {
	for _, failThrough := range []int{1, 3} {
		t.Run(fmt.Sprintf("fail_%d_attempts", failThrough), func(t *testing.T) {
			f := setup(t)
			ticket := addNode(t, f, "RETRY-534", "ticket", "Retry atomically", &f.project)
			closeAged(t, f, ticket, "done", "1 hour")
			// A sequence survives rollback. Fail at the final cursor write, after
			// tags, nominations and events, so retry atomicity is observable.
			_, err := f.db.Admin.Exec(t.Context(), fmt.Sprintf(`
			 CREATE SEQUENCE tagger_test_attempt;
			 GRANT USAGE ON SEQUENCE tagger_test_attempt TO %s;
			 CREATE FUNCTION tagger_test_conflict() RETURNS trigger LANGUAGE plpgsql AS $$
			 BEGIN
			   IF nextval('tagger_test_attempt') <= %d THEN
			     RAISE EXCEPTION 'test deadlock' USING ERRCODE='40P01';
			   END IF;
			   RETURN NEW;
			 END $$;
			 CREATE TRIGGER tagger_test_conflict BEFORE INSERT ON method_learning_tag_cursor
			 FOR EACH ROW EXECUTE FUNCTION tagger_test_conflict();`, pgx.Identifier{f.db.Role}.Sanitize(), failThrough))
			if err != nil {
				t.Fatal(err)
			}
			n, err := tagTenant(t.Context(), f.db.App, f.a.TenantID)
			want, attempts := 1, 2
			if failThrough == 3 {
				want, attempts = 0, 3
				var pe *pgconn.PgError
				if !errors.As(err, &pe) || pe.Code != "40P01" {
					t.Fatalf("exhausted retries: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if n != want || nodeTagged(t, f, ticket) != (want == 1) {
				t.Fatalf("nominations=%d want=%d tagged=%v", n, want, nodeTagged(t, f, ticket))
			}
			var tried, nominations, updates, cursors int
			err = f.db.Admin.QueryRow(t.Context(), `SELECT
			 (SELECT last_value FROM tagger_test_attempt),
			 (SELECT count(*) FROM method_learning_nominations WHERE tenant_id=$1),
			 (SELECT count(*) FROM events WHERE tenant_id=$1 AND node_id=$2 AND metadata->>'job'='learning-tagger'),
			 (SELECT count(*) FROM method_learning_tag_cursor WHERE tenant_id=$1)`, f.a.TenantID, ticket).Scan(&tried, &nominations, &updates, &cursors)
			if err != nil || tried != attempts || nominations != want || updates != want || cursors != want {
				t.Fatalf("attempts=%d nominations=%d events=%d cursors=%d want=%d: %v", tried, nominations, updates, cursors, want, err)
			}
		})
	}
}

func TestRetryTagTenantConflictsAndCancellation(t *testing.T) {
	for _, code := range []string{"40P01", "40001", "23505"} {
		t.Run(code, func(t *testing.T) {
			calls := 0
			conflict := fmt.Errorf("wrapped conflict: %w", &pgconn.PgError{Code: code})
			n, err := retryTagTenant(t.Context(), func() (int, error) {
				calls++
				if calls == 2 {
					return 1, nil
				}
				return 10, conflict // Uncommitted counts must be discarded.
			})
			if code == "23505" {
				if calls != 1 || n != 0 || !errors.Is(err, conflict) {
					t.Fatalf("non-retryable result: calls=%d n=%d err=%v", calls, n, err)
				}
			} else if calls != 2 || n != 1 || err != nil {
				t.Fatalf("retry result: calls=%d n=%d err=%v", calls, n, err)
			}
		})
	}
	t.Run("cancel_during_backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		n, err := retryTagTenant(ctx, func() (int, error) {
			calls++
			cancel()
			return 10, &pgconn.PgError{Code: "40P01"}
		})
		if calls != 1 || n != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled retry: calls=%d n=%d err=%v", calls, n, err)
		}
	})
}
