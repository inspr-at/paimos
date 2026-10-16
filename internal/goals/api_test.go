// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/reportercontract"
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
		if r.Header().Get("Aeon-Contract") != reportercontract.Goals {
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

// Risk: one hidden sibling prevents a legitimate project writer updating a
// visible outcome, or a replace silently destroys siblings the writer cannot see.
func TestGoalProjectWriterUpdatesOnlyVisibleOutcomes(t *testing.T) {
	f := writerFixture(t)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='member') WHERE tenant_id=$1 AND principal_id=$2`, f.reader.TenantID, f.reader.ID); err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, func() time.Time { return apiClock }}
	path := "/api/goals/" + f.goal
	response := goalRequest(t.Context(), m, f.reader, "PATCH", path, "project-visible", `{"items":{"upsert":[{"ref":"visible","note":"updated in project"}]}}`)
	readWriteResult(t, response, 200)
	var latest int64
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.reader), f.db.App, f.reader.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT max(id) FROM events WHERE node_id=$1 AND type LIKE 'goal.%'`, f.goal).Scan(&latest)
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"if_read_through": latest, "items": map[string]any{"mode": "replace"}})
	rejected := goalRequest(t.Context(), m, f.reader, "PATCH", path, "project-replace", string(raw))
	if rejected.Code != 403 {
		t.Fatalf("partial replace %d %s", rejected.Code, rejected.Body)
	}
	var note string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT fields->>'note' FROM nodes WHERE tenant_id=$1 AND id=$2`, f.writer.TenantID, f.outcome).Scan(&note); err != nil || note != "updated in project" {
		t.Fatalf("visible write %q %v", note, err)
	}
}

// Risk: an existing release cannot receive its first outcome before any goal
// create has seeded kinds, or a tenant kind edit permanently breaks updates.
func TestGoalReleaseFirstUpdateRestoresKinds(t *testing.T) {
	d := dbtest.Open(t)
	p := tenant.Principal{Kind: tenant.Person}
	var release string
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('release-first-goals','Release goals') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Builder') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, p.TenantID, p.ID, "admin")
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		project := fixtureNode(t, tx, p.TenantID, "project", "PROJ-1", "", nil)
		release = fixtureNode(t, tx, p.TenantID, "release", "REL-1", project, nil)
		_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=ARRAY['work']::text[] WHERE slug='release'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &module{d.App, func() time.Time { return apiClock }}
	readWriteResult(t, goalRequest(t.Context(), m, p, "PATCH", "/api/goals/"+release, "release-first", `{"items":{"upsert":[{"ref":"first","label":"First"}]}}`), 200)
	if _, err := d.Admin.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=ARRAY['work']::text[] WHERE tenant_id=$1 AND slug='release'`, p.TenantID); err != nil {
		t.Fatal(err)
	}
	readWriteResult(t, goalRequest(t.Context(), m, p, "PATCH", "/api/goals/"+release, "release-repair", `{"items":{"upsert":[{"ref":"first","note":"after kind edit"}]}}`), 200)
	var kinds int
	var children []string
	if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM node_kinds WHERE tenant_id=$1 AND slug IN ('goal','outcome')`, p.TenantID).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(t.Context(), `SELECT allowed_child_kinds FROM node_kinds WHERE tenant_id=$1 AND slug='release'`, p.TenantID).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if kinds != 2 || !slices.Contains(children, "work") || !slices.Contains(children, "outcome") {
		t.Fatalf("lazy release kinds %d %v", kinds, children)
	}
}

// Risk: the goal's handover summary leaks a hidden outcome, or a second marker
// silently replaces the existing handover without an explicit clear.
func TestGoalHandedOverReadAndConflict(t *testing.T) {
	f := writerFixture(t)
	path := "/api/goals/" + f.goal
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "handover-one", `{"items":{"handed_over":{"ref":"visible","detail":"Plan retained"}}}`), 200)
	m := &module{f.db.App, func() time.Time { return apiClock }}
	g := detail(t, goalRequest(t.Context(), m, f.reader, "GET", path, "", ""))
	if g.HandedOver == nil || g.HandedOver.OutcomeID != f.outcome || g.HandedOver.Detail != "Plan retained" || g.HandedOver.By != f.writer.ID {
		t.Fatalf("handover %+v", g.HandedOver)
	}
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "handover-two", `{"items":{"handed_over":{"ref":"keyless"}}}`), 409)
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "handover-clear", `{"items":{"handed_over":null}}`), 200)
	readWriteResult(t, apiRequest(t, f, "PATCH", path, "handover-hidden", `{"items":{"handed_over":{"ref":"hidden","detail":"Hidden handover sentinel"}}}`), 200)
	g = detail(t, goalRequest(t.Context(), m, f.reader, "GET", path, "", ""))
	if g.HandedOver != nil {
		t.Fatalf("hidden handover %+v", g.HandedOver)
	}
}
