// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

type storageFixture struct {
	db                                                       *dbtest.DB
	writer, reader                                           tenant.Principal
	project, otherProject, ticket, hiddenTicket              string
	goal, secondGoal, workspaceGoal, hiddenGoal, emptyGoal   string
	outcome, hiddenOutcome, keylessOutcome, workspaceOutcome string
}

func newStorageFixture(t *testing.T) storageFixture {
	t.Helper()
	f := storageFixture{db: dbtest.Open(t)}
	if err := f.db.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES ('goals-storage','Goals storage') RETURNING id::text`).Scan(&f.writer.TenantID); err != nil {
		t.Fatal(err)
	}
	f.reader.TenantID = f.writer.TenantID
	f.writer.Kind, f.reader.Kind = tenant.Person, tenant.Person
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if _, _, err := ensureKinds(t.Context(), tx, f.writer.TenantID); err != nil {
			return err
		}
		for _, principal := range []*tenant.Principal{&f.writer, &f.reader} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name)
				VALUES ($1,'person','Goal fixture') RETURNING id::text`, f.writer.TenantID).Scan(&principal.ID); err != nil {
				return err
			}
		}
		f.project = fixtureNode(t, tx, f.writer.TenantID, "project", "PRJ-1", "", nil)
		f.otherProject = fixtureNode(t, tx, f.writer.TenantID, "project", "PRJ-2", "", nil)
		f.ticket = fixtureNode(t, tx, f.writer.TenantID, "work", "TKT-1", f.project, nil)
		f.hiddenTicket = fixtureNode(t, tx, f.writer.TenantID, "work", "TKT-2", f.otherProject, nil)
		f.goal = fixtureNode(t, tx, f.writer.TenantID, GoalKind, "GOAL-1", f.project, nil)
		f.secondGoal = fixtureNode(t, tx, f.writer.TenantID, GoalKind, "GOAL-2", f.project, nil)
		f.workspaceGoal = fixtureNode(t, tx, f.writer.TenantID, GoalKind, "GOAL-3", "", nil)
		f.hiddenGoal = fixtureNode(t, tx, f.writer.TenantID, GoalKind, "GOAL-4", f.project, nil)
		f.emptyGoal = fixtureNode(t, tx, f.writer.TenantID, GoalKind, "GOAL-5", f.project, nil)
		f.outcome = fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "OUT-1", f.goal, map[string]any{"outcome_ref": "visible"})
		f.hiddenOutcome = fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "OUT-2", f.goal,
			map[string]any{"outcome_ref": "hidden", "eta_text": "private-ETA-sentinel", "eta_at": "2026-10-17T04:00:00Z"})
		f.keylessOutcome = fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "OUT-3", f.goal, map[string]any{"outcome_ref": "keyless"})
		f.workspaceOutcome = fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "OUT-4", f.workspaceGoal, map[string]any{"outcome_ref": "workspace"})
		onlyHidden := fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "OUT-5", f.hiddenGoal, map[string]any{"outcome_ref": "only-hidden"})
		for _, link := range [][2]string{{f.outcome, f.ticket}, {f.hiddenOutcome, f.ticket}, {f.hiddenOutcome, f.hiddenTicket}, {onlyHidden, f.hiddenTicket}} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES ($1,$2,$3,'serves')`, f.writer.TenantID, link[0], link[1]); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
			SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, f.writer.TenantID, f.reader.ID, f.project)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func fixtureNode(t *testing.T, tx pgx.Tx, tenantID, slug, key, parent string, fields map[string]any) string {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var parentID any
	if parent != "" {
		parentID = parent
	}
	var id string
	err = tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields)
		SELECT $1,id,$3,$3,$4,$5::jsonb FROM node_kinds WHERE tenant_id=$1 AND slug=$2 RETURNING id::text`,
		tenantID, slug, key, parentID, raw).Scan(&id)
	if err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
	return id
}

// Risk: relation RLS hides a serves edge before outcome visibility checks it,
// leaking that outcome's ETA through ordinary node reads or event history.
// This exercises the real NOBYPASSRLS app role and a project-only non-actor.
func TestGoalStorageVisibility(t *testing.T) {
	f := newStorageFixture(t)
	var visibleEvent, pingEvent, hiddenEvent, hiddenRefEvent, workspaceEvent, noVisibleOutcomeEvent, emptyEvent int64
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		for _, event := range []struct {
			nodeID, kind string
			after        any
			id           *int64
		}{
			{f.goal, "goal.updated", map[string]any{"steps": []Step{{ID: "next", Text: "Next", State: "now"}}}, &visibleEvent},
			{f.goal, "goal.eta_changed", ChangePing{GoalID: f.goal, Watermark: Watermark{1, 1}}, &pingEvent},
			{f.hiddenOutcome, "goal.eta_changed", map[string]any{"text": "private-ETA-sentinel", "at": "2026-10-17T04:00:00Z"}, &hiddenEvent},
			{f.goal, "goal.items_changed", map[string]any{"ticket_node_ids": []string{f.hiddenTicket}}, &hiddenRefEvent},
			{f.workspaceGoal, "goal.updated", map[string]any{"steps": []Step{}}, &workspaceEvent},
			{f.hiddenGoal, "goal.updated", map[string]any{"steps": []Step{}}, &noVisibleOutcomeEvent},
			{f.emptyGoal, "goal.updated", map[string]any{"steps": []Step{}}, &emptyEvent},
		} {
			written, err := events.Append(t.Context(), tx, f.writer, events.Change{NodeID: &event.nodeID, Type: event.kind, After: event.after})
			if err != nil {
				return err
			}
			*event.id = written.ID
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithPrincipal(t.Context(), f.reader)
	err = db.InTenant(ctx, f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		var empty, nullArray, nullEntry, duplicates, hidden bool
		if err := tx.QueryRow(ctx, `SELECT aeon_event_refs_visible('{}'::uuid[]),
 aeon_event_refs_visible(NULL::uuid[]),aeon_event_refs_visible(ARRAY[NULL::uuid]),
 aeon_event_refs_visible(ARRAY[$1::uuid,$1::uuid,NULL::uuid]),
 aeon_event_refs_visible(ARRAY[$1::uuid,$2::uuid])`, f.ticket, f.hiddenTicket).Scan(&empty, &nullArray, &nullEntry, &duplicates, &hidden); err != nil {
			return err
		}
		if !empty || !nullArray || !nullEntry || !duplicates || hidden {
			t.Fatalf("reference membership semantics changed empty=%v null=%v null-entry=%v duplicates=%v hidden=%v", empty, nullArray, nullEntry, duplicates, hidden)
		}
		var ids []string
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(n.id::text ORDER BY n.id::text),'{}') FROM nodes n
			JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='outcome'`).Scan(&ids); err != nil {
			return err
		}
		want := []string{f.outcome, f.keylessOutcome}
		slices.Sort(want)
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("visible outcomes = %v, want %v (a hidden target must hide the whole outcome)", ids, want)
		}
		var workspace int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE id=$1`, f.workspaceGoal).Scan(&workspace); err != nil || workspace != 0 {
			t.Fatalf("project-only reader sees workspace goal: %d %v", workspace, err)
		}
		var relationIDs []string
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(source_node_id::text),'{}') FROM node_relations WHERE type='serves'`).Scan(&relationIDs); err != nil || len(relationIDs) != 1 || relationIDs[0] != f.outcome {
			t.Fatalf("hidden outcome's apparently visible serves end leaked: %v %v", relationIDs, err)
		}
		var eventsSeen []int64
		var payloads string
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(id ORDER BY id),'{}'),coalesce(string_agg(after::text,''),'') FROM events WHERE type LIKE 'goal.%'`).Scan(&eventsSeen, &payloads); err != nil {
			return err
		}
		if !reflect.DeepEqual(eventsSeen, []int64{visibleEvent, pingEvent}) || strings.Contains(payloads, "private-ETA-sentinel") || strings.Contains(payloads, "2026-10-17") {
			t.Fatalf("goal event leak or missing visible event: %v payloads=%s", eventsSeen, payloads)
		}
		// Replay uses the same event RLS, including hidden node_refs.
		var replay []int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(id ORDER BY id),'{}') FROM events WHERE type LIKE 'goal.%' AND id>$1`, visibleEvent-1).Scan(&replay); err != nil || !reflect.DeepEqual(replay, eventsSeen) {
			t.Fatalf("replay visibility differs: %v %v", replay, err)
		}
		var setting, path string
		if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.visible_projects'),coalesce(current_setting('aeon.outcome_visibility_path',true),'')`).Scan(&setting, &path); err != nil {
			return err
		}
		if setting != "{"+f.project+"}" || path != "" {
			t.Fatalf("visibility probe left widened settings: %q %q", setting, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Establish that the fixture retained the hidden facts; absence above must
	// be caused by policy, not by silently omitting the source data.
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		var ids []int64
		if err := tx.QueryRow(t.Context(), `SELECT array_agg(id ORDER BY id) FROM events WHERE type LIKE 'goal.%'`).Scan(&ids); err != nil {
			return err
		}
		if !reflect.DeepEqual(ids, []int64{visibleEvent, pingEvent, hiddenEvent, hiddenRefEvent, workspaceEvent, noVisibleOutcomeEvent, emptyEvent}) {
			t.Fatalf("fixture lost hidden events: %v", ids)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Risk: schema-only constraints release a deleted ref, permit two handovers,
// or make a new receipt visible in another tenant.
func TestGoalStorageIdentityAndReceipts(t *testing.T) {
	f := newStorageFixture(t)
	ctx := dbtest.Seed(t.Context())
	err := db.InTenant(ctx, f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, f.outcome); err != nil {
			return err
		}
		expectPGCode(t, tx, "23505", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields)
				SELECT tenant_id,kind_id,'OUT-6','Ref reuse',parent_id,fields FROM nodes WHERE id=$1`, f.outcome)
			return err
		})
		// The same ref is allowed on another goal.
		fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "OUT-6", f.secondGoal, map[string]any{"outcome_ref": "visible"})
		if _, err := tx.Exec(ctx, `UPDATE nodes SET fields=fields||'{"handed_over_at":"2026-10-16T20:00:00Z"}'::jsonb WHERE id=$1`, f.keylessOutcome); err != nil {
			return err
		}
		expectPGCode(t, tx, "23505", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE nodes SET fields=fields||'{"handed_over_at":"2026-10-16T20:00:00Z"}'::jsonb WHERE id=$1`, f.hiddenOutcome)
			return err
		})
		digest := make([]byte, 32)
		_, err := tx.Exec(ctx, `INSERT INTO goal_update_receipts
			(tenant_id,actor_principal_id,idempotency_key,method,target_node_id,request_sha256,response,event_id)
			VALUES ($1,$2,'stable-key-1','PATCH',$3,$4,'{"changed":true,"read_through":41,"generation":7}',41)`,
			f.writer.TenantID, f.writer.ID, f.goal, digest)
		if err != nil {
			return err
		}
		expectPGCode(t, tx, "23505", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO goal_update_receipts
				(tenant_id,actor_principal_id,idempotency_key,method,target_node_id,request_sha256,response,event_id)
				VALUES ($1,$2,'stable-key-1','PATCH',$3,$4,'{}',0)`, f.writer.TenantID, f.writer.ID, f.secondGoal, digest)
			return err
		})
		var method, target string
		var response map[string]any
		if err := tx.QueryRow(ctx, `SELECT method,target_node_id::text,response FROM goal_update_receipts WHERE idempotency_key='stable-key-1'`).Scan(&method, &target, &response); err != nil {
			return err
		}
		if method != "PATCH" || target != f.goal || response["generation"] != float64(7) || response["read_through"] != float64(41) {
			t.Fatalf("receipt lost original binding/response: %s %s %v", method, target, response)
		}
		// Goal and outcome are outside aeon_work_scope's eligible leaf kinds.
		var leaves int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM aeon_work_scope(ARRAY[$1::uuid]) WHERE is_leaf`, f.goal).Scan(&leaves); err != nil || leaves != 0 {
			t.Fatalf("outcomes entered the work tree: %d %v", leaves, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var otherTenant string
	if err := f.db.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES ('goals-other','Other') RETURNING id::text`).Scan(&otherTenant); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(ctx, f.db.App, otherTenant, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM goal_update_receipts`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("receipt crosses tenants: %d %v", count, err)
		}
		expectPGCode(t, tx, "42501", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO goal_update_receipts
				(tenant_id,actor_principal_id,idempotency_key,method,target_node_id,request_sha256,response,event_id)
				VALUES ($1,$2,'other-tenant-key','PATCH',$3,$4,'{}',0)`, f.writer.TenantID, f.writer.ID, f.goal, make([]byte, 32))
			return err
		})
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE id=$1`, f.goal).Scan(&count); err != nil || count != 0 {
			t.Fatalf("goal crosses tenants: %d %v", count, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func expectPGCode(t *testing.T, tx pgx.Tx, code string, write func(pgx.Tx) error) {
	t.Helper()
	savepoint, err := tx.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = write(savepoint)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("got %v, want SQLSTATE %s", err, code)
	}
	if err := savepoint.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// Risk: lazy kinds reset tenant presentation, wipe release child kinds, seed
// sample nodes, or turn outcomes into work kinds.
func TestGoalLazyKindSeeding(t *testing.T) {
	d := dbtest.Open(t)
	var tenantID string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES ('goals-kinds','Kinds') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		ctx := t.Context()
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM node_kinds WHERE slug IN ('goal','outcome')`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration seeded kinds: %d %v", count, err)
		}
		kinds, changes, err := ensureKinds(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		if len(changes) != 2 || kinds[GoalKind].Prefix != "GOAL" || kinds[OutcomeKind].Prefix != "OUT" || kinds["release"].AllowedChildKinds != nil {
			t.Fatalf("initial kinds: %+v %+v", kinds, changes)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("kind seed created sample nodes: %d %v", count, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE node_kinds SET label='Ziel',short_prefix='ZIEL',icon='flag',allowed_child_kinds='{}',
			field_schema=field_schema||'{"properties":{"custom":{"type":"string"}}}'::jsonb WHERE slug='goal'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE node_kinds SET allowed_child_kinds='{work}' WHERE slug='release'`); err != nil {
			return err
		}
		next, changes, err := ensureKinds(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		goal := next[GoalKind]
		if goal.ID != kinds[GoalKind].ID || goal.Label != "Ziel" || goal.Prefix != "ZIEL" || goal.Icon != "flag" || !reflect.DeepEqual(goal.AllowedChildKinds, []string{OutcomeKind}) {
			t.Fatalf("lazy repair reset tenant presentation: %+v", goal)
		}
		if len(changes) != 2 || !reflect.DeepEqual(next["release"].AllowedChildKinds, []string{"work", "outcome"}) || !strings.Contains(string(goal.FieldSchema), `"custom"`) || strings.Contains(string(next[OutcomeKind].FieldSchema), "issue_family") {
			t.Fatalf("non-additive schema/children repair: %+v %+v", next, changes)
		}
		if _, changes, err := ensureKinds(ctx, tx, tenantID); err != nil || len(changes) != 0 {
			return fmt.Errorf("non-idempotent seed: changes=%d err=%v", len(changes), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A node may be a reference-only serves target. Visibility must remain
// transitive for outcome targets, and malformed cycles must terminate closed.
func TestGoalOutcomeReferenceVisibilityIsBounded(t *testing.T) {
	f := newStorageFixture(t)
	ctx := dbtest.Seed(t.Context())
	var indirect, cycleA, cycleB string
	err := db.InTenant(ctx, f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		indirect = fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "OUT-6", f.goal, map[string]any{"outcome_ref": "indirect"})
		cycleA = fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "OUT-7", f.goal, map[string]any{"outcome_ref": "cycle-a"})
		cycleB = fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, "OUT-8", f.goal, map[string]any{"outcome_ref": "cycle-b"})
		for _, link := range [][2]string{{indirect, f.hiddenOutcome}, {cycleA, cycleB}, {cycleB, cycleA}} {
			if _, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES ($1,$2,$3,'serves')`, f.writer.TenantID, link[0], link[1]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	read := tenant.WithPrincipal(t.Context(), f.reader)
	err = db.InTenant(read, f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SET LOCAL statement_timeout='5s'`); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE id=ANY($1::uuid[])`, []string{indirect, cycleA, cycleB}).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("hidden indirect reference or cycle became visible: %d", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
