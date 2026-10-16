// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var apiClock = time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)

func writerFixture(t *testing.T) storageFixture {
	t.Helper()
	f := newStorageFixture(t)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='admin'`, f.writer.TenantID, f.writer.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func goalRequest(ctx context.Context, m *module, p tenant.Principal, method, path, key, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	m.Mount(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(tenant.WithPrincipal(ctx, p))
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
func apiRequest(t *testing.T, f storageFixture, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	return goalRequest(t.Context(), &module{f.db.App, func() time.Time { return apiClock }}, f.writer, method, path, key, body)
}
func readWriteResult(t *testing.T, r *httptest.ResponseRecorder, status int) WriteResult {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status %d want %d: %s", r.Code, status, r.Body)
	}
	var out WriteResult
	if status == 200 {
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if r.Header().Get("Aeon-Contract") != "goals/1.0" {
			t.Fatal("missing contract")
		}
	}
	return out
}
func eventCount(t *testing.T, f storageFixture) int {
	t.Helper()
	var count int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1`, f.writer.TenantID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// Risk: retry duplication, cross-target receipt reuse, a changed request, and
// invalid nested refs must never partially commit or return a stored body.
func TestGoalAPIAtomicReceipts(t *testing.T) {
	f := writerFixture(t)
	path := "/api/goals/" + f.secondGoal
	body := `{"items":{"upsert":[{"ref":"one","label":"One","keys":["TKT-1"]}]},"eta":{"text":"tonight"}}`
	first := readWriteResult(t, apiRequest(t, f, "PATCH", path, "update-key-01", body), 200)
	if !first.Changed || first.ReadThrough == 0 || first.Generation < 1 {
		t.Fatalf("write %+v", first)
	}
	count := eventCount(t, f)
	replay := readWriteResult(t, apiRequest(t, f, "PATCH", path, "update-key-01", body), 200)
	if !reflect.DeepEqual(first, replay) || eventCount(t, f) != count {
		t.Fatal("replay changed result or wrote events")
	}
	readWriteResult(t, apiRequest(t, f, "PATCH", "/api/goals/"+f.goal, "update-key-01", body), 409)
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "update-key-01", `{"eta":null}`), 409)
	unchanged := readWriteResult(t, apiRequest(t, f, "PATCH", path, "update-key-02", body), 200)
	if unchanged.Changed || eventCount(t, f) != count {
		t.Fatal("same body under new key wrote domain events")
	}
	bad := `{"items":{"upsert":[{"ref":"two","label":"Two"},{"ref":"bad","label":"Bad","keys":["TKT-999"]}]}}`
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "update-key-bad", bad), 404)
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "update-no-ref", `{"items":{"upsert":[{"label":"no ref"}]}}`), 400)
	if eventCount(t, f) != count {
		t.Fatal("invalid batch committed an event")
	}
	var leaked bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM nodes WHERE tenant_id=$1 AND parent_id=$2 AND fields->>'outcome_ref'='two')`, f.writer.TenantID, f.secondGoal).Scan(&leaked); err != nil || leaked {
		t.Fatalf("partial node commit %v %v", leaked, err)
	}
}

// Risk: pre-fence authorization and old receipts can outlive a concurrent
// authority revocation. A database barrier establishes the exact interleaving.
func TestGoalAPIReplayReauthorizesUnderFence(t *testing.T) {
	f := writerFixture(t)
	path := "/api/goals/" + f.goal
	body := `{"eta":{"text":"today"}}`
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "revoke-key-01", body), 200)
	count := eventCount(t, f)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.db.App, func(sql string) bool { return strings.Contains(sql, "aeon_enter_principal") })
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- goalRequest(ctx, &module{pool, func() time.Time { return apiClock }}, f.writer, "PATCH", path, "revoke-key-01", body)
	}()
	barrier.Wait(t, ctx) // InTenant entered with the old visibility, before LockTree.
	err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, f.writer.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='viewer') WHERE tenant_id=$1 AND principal_id=$2`, f.writer.TenantID, f.writer.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	barrier.Release()
	response := dbtest.Await(t, ctx, done)
	if response.Code != 403 || strings.Contains(response.Body.String(), `"changed"`) {
		t.Fatalf("revoked receipt: %d %s", response.Code, response.Body)
	}
	if eventCount(t, f) != count {
		t.Fatal("revoked replay wrote events")
	}
}

// Risk: create replays must bind the original target, reject method reuse,
// and check the target's current scope and existence rather than the request.
func TestGoalAPICreateAndCurrentTarget(t *testing.T) {
	f := writerFixture(t)
	body := `{"title":"New goal","project_id":"` + f.project + `"}`
	first := readWriteResult(t, apiRequest(t, f, "POST", "/api/goals", "create-key-01", body), 200)
	repeat := readWriteResult(t, apiRequest(t, f, "POST", "/api/goals", "create-key-01", body), 200)
	if !reflect.DeepEqual(first, repeat) || first.ID == "" {
		t.Fatal("create replay did not retain identity")
	}
	readWriteResult(t, apiRequest(t, f, "PATCH", "/api/goals/"+first.ID, "create-key-01", `{}`), 409)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, f.writer.TenantID, first.ID); err != nil {
		t.Fatal(err)
	}
	readWriteResult(t, apiRequest(t, f, "POST", "/api/goals", "create-key-01", body), 404)
}

// Risk: a move breaks immutable history or reuses an old source ref; replace
// can remove concurrent work unless it checks the goal event watermark.
func TestGoalAPIMoveReplaceAndRetiredRefs(t *testing.T) {
	f := writerFixture(t)
	source := "/api/goals/" + f.goal
	dest := "/api/goals/" + f.secondGoal
	move := `{"items":{"move":[{"outcome_id":"` + f.outcome + `","to_goal":"` + f.secondGoal + `"}]}}`
	first := readWriteResult(t, apiRequest(t, f, "PATCH", source, "move-key-001", move), 200)
	second := readWriteResult(t, apiRequest(t, f, "PATCH", source, "move-key-001", move), 200)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("move replay mutated")
	}
	readWriteResult(t, apiRequest(t, f, "PATCH", source, "move-key-002", move), 404)
	readWriteResult(t, apiRequest(t, f, "PATCH", source, "retired-key1", `{"items":{"upsert":[{"ref":"visible","label":"Cannot reuse"}]}}`), 409)
	readWriteResult(t, apiRequest(t, f, "PATCH", dest, "replace-bad1", `{"if_read_through":0,"items":{"mode":"replace"}}`), 409)
	var latest int64
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT max(id) FROM events WHERE tenant_id=$1 AND node_id=$2 AND type LIKE 'goal.%'`, f.writer.TenantID, f.secondGoal).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"if_read_through": latest, "items": map[string]any{"mode": "replace"}})
	readWriteResult(t, apiRequest(t, f, "PATCH", dest, "replace-good", string(raw)), 200)
	readWriteResult(t, apiRequest(t, f, "PATCH", dest, "retired-key2", `{"items":{"upsert":[{"ref":"visible","label":"Cannot reuse"}]}}`), 409)
}

// Risk: a sampling job loses A→B→A, while a goal-level derived ETA can expose
// another project's value. Real write transactions must preserve both edges.
func TestGoalAPIWriteTimeETAsAndHiddenPings(t *testing.T) {
	f := writerFixture(t)
	path := "/api/goals/" + f.goal
	for i, text := range []string{"A", "B", "A"} {
		body := `{"items":{"upsert":[{"ref":"visible","eta":{"text":"` + text + `"}}]}}`
		key := []string{"eta-write-A1", "eta-write-B1", "eta-write-A2"}[i]
		readWriteResult(t, apiRequest(t, f, "PATCH", path, key, body), 200)
	}
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.writer), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT after->>'text' FROM events WHERE node_id=$1 AND type='goal.eta_changed' ORDER BY id`, f.outcome)
		if err != nil {
			return err
		}
		texts := []string{}
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				return err
			}
			texts = append(texts, text)
		}
		err = rows.Err()
		rows.Close()
		if !reflect.DeepEqual(texts, []string{"A", "B", "A"}) {
			t.Fatalf("write-time ETA edges %v", texts)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "hidden-eta-01", `{"items":{"upsert":[{"ref":"hidden","eta":{"text":"new-private-sentinel"}}]}}`), 200)
	err = db.InTenant(tenant.WithPrincipal(t.Context(), f.reader), f.db.App, f.reader.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT node_id::text,after FROM events WHERE type='goal.eta_changed' ORDER BY id`)
		if err != nil {
			return err
		}
		pings := 0
		for rows.Next() {
			var id string
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				return err
			}
			if strings.Contains(string(raw), "private") {
				t.Fatal("hidden ETA leaked")
			}
			if id == f.goal {
				var payload map[string]any
				json.Unmarshal(raw, &payload)
				if len(payload) != 3 || payload["goal_id"] != f.goal || payload["generation"] == nil || payload["read_through"] == nil {
					t.Fatalf("derived payload %s", raw)
				}
				pings++
			}
		}
		err = rows.Err()
		rows.Close()
		if pings != 4 {
			t.Fatalf("visible pings %d", pings)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
