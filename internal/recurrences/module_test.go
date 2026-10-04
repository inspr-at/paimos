// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fixture struct {
	t               *testing.T
	d               *dbtest.DB
	p               tenant.Principal
	project, parent string
	m               *Module
	handler         http.Handler
	now             time.Time
}

func setup(t *testing.T) *fixture {
	f := &fixture{t: t, d: dbtest.Open(t), now: timestamp(t, "2026-10-02T12:00:00Z"), p: tenant.Principal{TenantID: "10000000-0000-4000-8000-000000000001", Kind: tenant.Person, Name: "Owner"}}
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'recurring','Recurring')`, f.p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, f.p.TenantID).Scan(&f.p.ID)
	})
	dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "owner")
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'tag','Tag','TAG','tag') ON CONFLICT DO NOTHING`, f.p.TenantID)
		return err
	})
	f.project = f.node("project", nil, "Project")
	f.parent = f.node("work", &f.project, "Code health")
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"project_key":"REC"}'::jsonb WHERE id=$1`, f.project)
		return err
	})
	f.install(New(f.d.App))
	return f
}
func (f *fixture) install(m *Module) {
	m.now = func(context.Context, pgx.Tx) (time.Time, error) { return f.now, nil }
	f.m = m
	mux := http.NewServeMux()
	m.Mount(mux)
	f.handler = mux
}
func (f *fixture) tx(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := db.InTenant(dbtest.Seed(f.t.Context()), f.d.App, f.p.TenantID, fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) node(kind string, parent *string, title string) string {
	f.t.Helper()
	var id string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,position) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),$2,$3,coalesce((SELECT max(position)+1024 FROM nodes WHERE parent_id IS NOT DISTINCT FROM $3::uuid),1024) FROM node_kinds k WHERE slug=$4 RETURNING id::text`, f.p.TenantID, title, parent, kind).Scan(&id)
	})
	return id
}
func (f *fixture) input() Input {
	return Input{ProjectID: f.project, ParentID: f.parent, Template: Template{Title: "Sweep {{occurrence}} on {{date}}", Description: "Read the code", Criteria: []string{"Verify findings"}, EstimateHours: 2, Priority: "high", Type: "ticket"}, Trigger: Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=MO", TimeOfDay: "09:00", Timezone: "Europe/Vienna"}, OverlapPolicy: "skip", CatchUpPolicy: "one"}
}
func (f *fixture) call(p tenant.Principal, method, path string, body any, status int) []byte {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
	out := httptest.NewRecorder()
	f.handler.ServeHTTP(out, req)
	if out.Code != status {
		f.t.Fatalf("%s %s: %d %s want %d", method, path, out.Code, out.Body.String(), status)
	}
	return out.Body.Bytes()
}
func (f *fixture) create(in Input) Recurrence {
	f.t.Helper()
	var r Recurrence
	raw := f.call(f.p, "POST", "/api/recurrences", in, 201)
	if err := json.Unmarshal(raw, &r); err != nil {
		f.t.Fatal(err)
	}
	return r
}
func (f *fixture) get(id string) Recurrence {
	f.t.Helper()
	var r Recurrence
	raw := f.call(f.p, "GET", "/api/recurrences/"+id, nil, 200)
	if err := json.Unmarshal(raw, &r); err != nil {
		f.t.Fatal(err)
	}
	return r
}
func (f *fixture) manual(id, key string) Occurrence {
	f.t.Helper()
	var o Occurrence
	raw := f.call(f.p, "POST", "/api/recurrences/"+id+"/run-now", map[string]string{"idempotency_key": key}, 200)
	if err := json.Unmarshal(raw, &o); err != nil {
		f.t.Fatal(err)
	}
	return o
}
func (f *fixture) receipts(id string) []Occurrence {
	f.t.Helper()
	out := []Occurrence{}
	f.tx(func(tx pgx.Tx) error {
		rows, err := tx.Query(f.t.Context(), `SELECT `+occurrenceColumns+` FROM recurrence_occurrences WHERE recurrence_id=$1 ORDER BY number`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOccurrence(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out
}
func (f *fixture) run() {
	f.t.Helper()
	if err := f.m.RunTenant(f.t.Context(), f.p.TenantID); err != nil {
		f.t.Fatal(err)
	}
}
func TestManualReceiptsOverlapProvenanceAndQueue(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.QueueEach = true
	tag := f.node("tag", nil, "Health")
	in.Template.Tags = []string{tag}
	r := f.create(in)
	first := f.manual(r.ID, "first")
	if first.Outcome != "created" || first.NodeID == nil || first.Number != 1 {
		t.Fatalf("first %+v", first)
	}
	retry := f.manual(r.ID, "first")
	if retry.Key != first.Key || retry.Number != 1 || *retry.NodeID != *first.NodeID {
		t.Fatalf("retry %+v", retry)
	}
	second := f.manual(r.ID, "second")
	if second.Outcome != "skipped" || second.Reason != "previous_occurrence_open" {
		t.Fatalf("overlap %+v", second)
	}
	f.tx(func(tx pgx.Tx) error {
		var title, actor string
		var fields []byte
		var runs int
		if err := tx.QueryRow(t.Context(), `SELECT n.title,n.fields,p.name FROM nodes n JOIN events e ON e.node_id=n.id AND e.type='node.created' JOIN principals p ON p.id=e.actor_principal_id WHERE n.id=$1`, *first.NodeID).Scan(&title, &fields, &actor); err != nil {
			return err
		}
		if actor != "Recurring work" || title != "Sweep 1 on 2026-10-02" || !strings.Contains(string(fields), "Health") || !strings.Contains(string(fields), r.ID) {
			t.Fatalf("provenance %s %s %s", title, actor, fields)
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1 AND status='queued' AND queue_target_agent_id IS NULL AND queue_by_principal_id=(SELECT id FROM principals WHERE name='Recurring work')`, first.NodeID).Scan(&runs); err != nil {
			return err
		}
		if runs != 1 {
			t.Fatalf("queued %d", runs)
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='accepted' WHERE id=$1`, first.NodeID)
		return err
	})
	third := f.manual(r.ID, "third")
	if third.Outcome != "created" || third.Number != 3 {
		t.Fatalf("closed overlap %+v", third)
	}
	if len(f.receipts(r.ID)) != 3 {
		t.Fatal("receipt count")
	}
	current := f.get(r.ID)
	in = current.Input
	in.Template.Title = "Revised"
	body := map[string]any{}
	raw, _ := json.Marshal(in)
	_ = json.Unmarshal(raw, &body)
	body["expected_revision"] = current.Revision
	f.call(f.p, "PUT", "/api/recurrences/"+r.ID, body, 200)
	if replay := f.manual(r.ID, "first"); *replay.NodeID != *first.NodeID {
		t.Fatal("template edit invalidated key")
	}
}
func TestCatchUpReplicaRaceRestartAndPause(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.OverlapPolicy = "create"
	r := f.create(in)
	// Six weeks of downtime create only the latest due Monday, not six tickets.
	f.now = timestamp(t, "2026-11-17T12:00:00Z")
	if _, err := ensureActor(t.Context(), f.m, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	barrier := make(chan struct{})
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier
			replica := New(f.d.App)
			replica.now = f.m.now
			errs <- replica.RunTenant(t.Context(), f.p.TenantID)
		}()
	}
	close(barrier)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := f.receipts(r.ID)
	if len(got) != 1 || got[0].ScheduledAt.Format(time.RFC3339) != "2026-11-16T08:00:00Z" {
		t.Fatalf("catchup %+v", got)
	}
	f.install(New(f.d.App))
	f.run()
	if len(f.receipts(r.ID)) != 1 {
		t.Fatal("restart duplicated")
	}
	current := f.get(r.ID)
	f.call(f.p, "POST", "/api/recurrences/"+r.ID+"/pause", map[string]int64{"expected_revision": current.Revision}, 200)
	f.now = timestamp(t, "2026-12-01T12:00:00Z")
	f.run()
	if len(f.receipts(r.ID)) != 1 {
		t.Fatal("paused schedule ran")
	}
	current = f.get(r.ID)
	f.call(f.p, "POST", "/api/recurrences/"+r.ID+"/resume", map[string]int64{"expected_revision": current.Revision}, 200)
	f.run()
	if len(f.receipts(r.ID)) != 1 {
		t.Fatal("resume caught paused work")
	}
	f.now = timestamp(t, "2026-12-07T08:00:00Z")
	f.run()
	if len(f.receipts(r.ID)) != 2 {
		t.Fatal("resumed schedule did not run")
	}
}
func TestDatabaseClockPreviewAndForegroundClaim(t *testing.T) {
	f := setup(t)
	r := f.create(f.input())
	f.now = *r.NextAt
	actor, err := ensureActor(t.Context(), f.m, f.p.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
			if _, err := lock(t.Context(), tx, f.p.TenantID, false); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	err = f.m.RunTenant(t.Context(), actor.TenantID)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if len(f.receipts(r.ID)) != 0 {
		t.Fatal("worker did not yield to claim")
	}
	f.run()
	if len(f.receipts(r.ID)) != 1 {
		t.Fatal("released claim lost work")
	}
	before := f.get(r.ID)
	raw := f.call(f.p, "GET", "/api/recurrences/"+r.ID+"/preview?count=2&after=2026-10-02T12:00:00Z", nil, 200)
	if !strings.Contains(string(raw), "2026-10-05T07:00:00Z") {
		t.Fatalf("preview %s", raw)
	}
	if after := f.get(r.ID); after.Revision != before.Revision || after.OccurrenceCount != before.OccurrenceCount {
		t.Fatal("preview wrote state")
	}
	f.call(f.p, "GET", "/api/recurrences/"+r.ID+"/preview?count=101", nil, 400)
}
func TestPublicationEventsProductHistoryAndNoDowntimeBurst(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.Trigger = Trigger{Kind: "event", Event: "release.published"}
	in.OverlapPolicy = "create"
	in.Template.Title = "Delta {{release_name}} {{release_version}} #{{occurrence}}"
	r := f.create(in)
	f.now = f.now.Add(time.Hour)
	f.m.WithHistory([]Publication{{ProjectKey: "OTHER", Name: "Invisible", Version: "v0", PublishedAt: f.now}, {ProjectKey: "REC", Name: "First", Version: "v1", PublishedAt: f.now}, {ProjectKey: "REC", Name: "Second", Version: "v2", PublishedAt: f.now.Add(time.Minute)}})
	f.run()
	if len(f.receipts(r.ID)) != 1 {
		t.Fatal("first event missing")
	}
	f.now = f.now.Add(2 * time.Minute)
	f.run()
	f.run()
	if len(f.receipts(r.ID)) != 2 {
		t.Fatal("history repeat duplicated")
	}
	// Several explicit events accumulated during downtime coalesce to the newest.
	f.tx(func(tx pgx.Tx) error {
		for i := 3; i < 6; i++ {
			if _, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &f.project, Type: "release.published", After: Publication{ProjectID: f.project, Name: fmt.Sprintf("Release %d", i), Version: fmt.Sprintf("v%d", i), PublishedAt: f.now}}); err != nil {
				return err
			}
		}
		return nil
	})
	f.run()
	got := f.receipts(r.ID)
	if len(got) != 3 || got[2].SourceEventID == nil {
		t.Fatalf("event receipts %+v", got)
	}
	var title string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT title FROM nodes WHERE id=$1`, got[2].NodeID).Scan(&title)
	})
	if !strings.Contains(title, "Release 5 v5 #3") {
		t.Fatal(title)
	}
	// A journey row for the same product/version must not create another event.
	release := f.node("release", &f.project, "Second")
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, f.p.TenantID, f.project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,project_node_id,release_node_id,number,state,version_scheme,version,released_at) VALUES($1,$2,$3,1,'released','legacy','v2',$4)`, f.p.TenantID, f.project, release, f.now)
		return err
	})
	f.run()
	if len(f.receipts(r.ID)) != 3 {
		t.Fatal("journey/history duplicate")
	}
	f.call(f.p, "GET", "/api/recurrences/"+r.ID+"/preview", nil, 200)
}
func TestPauseDiscardsUnsyncedPublicationAndHistoricalDiscovery(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.Trigger = Trigger{Kind: "event", Event: "release.published"}
	r := f.create(in)
	f.m.WithHistory([]Publication{{ProjectKey: "REC", Version: "historical", PublishedAt: f.now.Add(-time.Hour)}})
	f.run()
	if len(f.receipts(r.ID)) != 0 {
		t.Fatal("old release fired")
	}
	f.call(f.p, "POST", "/api/recurrences/"+r.ID+"/pause", map[string]int64{"expected_revision": r.Revision}, 200)
	duringPause := f.now.Add(time.Hour)
	f.now = f.now.Add(2 * time.Hour)
	current := f.get(r.ID)
	f.call(f.p, "POST", "/api/recurrences/"+r.ID+"/resume", map[string]int64{"expected_revision": current.Revision}, 200)
	f.m.WithHistory([]Publication{{ProjectKey: "REC", Version: "during-pause", PublishedAt: duringPause}})
	f.run()
	if len(f.receipts(r.ID)) != 0 {
		t.Fatal("unsynced paused publication fired")
	}
	f.now = f.now.Add(time.Hour)
	f.m.WithHistory([]Publication{{ProjectKey: "REC", Version: "new", PublishedAt: f.now}})
	f.run()
	if len(f.receipts(r.ID)) != 1 {
		t.Fatal("new publication missing")
	}
}
func TestPermissionsExplicitAgentGrantTenantIsolationAndRLS(t *testing.T) {
	f := setup(t)
	r := f.create(f.input())
	var agentID, viewerID string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Agent') RETURNING id::text`, f.p.TenantID).Scan(&agentID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Viewer') RETURNING id::text`, f.p.TenantID).Scan(&viewerID)
	})
	dbtest.BindRole(t, f.d, f.p.TenantID, agentID, "owner")
	dbtest.BindRole(t, f.d, f.p.TenantID, viewerID, "viewer")
	agent := tenant.Principal{ID: agentID, TenantID: f.p.TenantID, Kind: tenant.Agent, Scopes: []string{Permission, "nodes.write", "run.create", "work_orders.write"}, KeyCreatorID: f.p.ID}
	f.call(agent, "GET", "/api/recurrences/"+r.ID, nil, 403)
	f.call(tenant.Principal{ID: viewerID, TenantID: f.p.TenantID, Kind: tenant.Person}, "GET", "/api/recurrences/"+r.ID, nil, 403)
	var roleID string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'recurring_agent','Explicit recurring agent') RETURNING id::text`, f.p.TenantID).Scan(&roleID); err != nil {
			return err
		}
		for _, perm := range []string{Permission, "nodes.read", "nodes.write"} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, f.p.TenantID, roleID, perm); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace'`, agentID, roleID)
		return err
	})
	f.call(agent, "GET", "/api/recurrences/"+r.ID, nil, 200)
	f.call(agent, "POST", "/api/recurrences", f.input(), 201)
	agent.Scopes = []string{"nodes.read"}
	f.call(agent, "GET", "/api/recurrences/"+r.ID, nil, 403)
	// A second tenant cannot read or mutate either new table using known UUIDs.
	foreign := tenant.Principal{TenantID: "20000000-0000-4000-8000-000000000001", Kind: tenant.Person}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreign.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'foreign','Foreign')`, foreign.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Foreign') RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, foreign.TenantID, foreign.ID, "owner")
	f.manual(r.ID, "tenant-test")
	f.call(foreign, "GET", "/api/recurrences/"+r.ID, nil, 404)
	f.call(foreign, "POST", "/api/recurrences/"+r.ID+"/pause", map[string]int64{"expected_revision": 1}, 404)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreign.TenantID, func(tx pgx.Tx) error {
		for _, table := range []string{"recurrences", "recurrence_occurrences"} {
			var count int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatalf("foreign %s count %d", table, count)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.tx(func(tx pgx.Tx) error {
		var enabled, forced bool
		for _, name := range []string{"recurrences", "recurrence_occurrences"} {
			if err := tx.QueryRow(t.Context(), `SELECT relrowsecurity,relforcerowsecurity FROM pg_class WHERE relname=$1`, name).Scan(&enabled, &forced); err != nil {
				return err
			}
			if !enabled || !forced {
				t.Fatalf("RLS %s", name)
			}
		}
		return nil
	})
	if perm, ok := authz.Lookup(Permission); !ok || !perm.AgentGrantable {
		t.Fatal("grant catalog missing")
	}
}
func TestProjectOnlyGrantAndIDOR(t *testing.T) {
	f := setup(t)
	r := f.create(f.input())
	other := f.node("project", nil, "Other")
	parent := f.node("work", &other, "Other epic")
	in := f.input()
	in.ProjectID = other
	in.ParentID = parent
	hidden := f.create(in)
	var personID string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project member') RETURNING id::text`, f.p.TenantID).Scan(&personID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE key='member'`, f.p.TenantID, personID, f.project)
		return err
	})
	p := tenant.Principal{ID: personID, TenantID: f.p.TenantID, Kind: tenant.Person}
	f.call(p, "GET", "/api/recurrences/"+r.ID, nil, 200)
	f.call(p, "GET", "/api/recurrences/"+hidden.ID, nil, 404)
	raw := f.call(p, "GET", "/api/recurrences", nil, 200)
	if strings.Contains(string(raw), hidden.ID) || !strings.Contains(string(raw), r.ID) {
		t.Fatalf("list leak %s", raw)
	}
	f.call(p, "POST", "/api/recurrences", in, 403)
}
func TestRollbackUniqueKeyAndInvalidTarget(t *testing.T) {
	f := setup(t)
	r := f.create(f.input())
	actor, err := ensureActor(t.Context(), f.m, f.p.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	abort := errors.New("injected rollback")
	err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		if _, err := lock(t.Context(), tx, f.p.TenantID, false); err != nil {
			return err
		}
		if _, err := occur(t.Context(), tx, actor, r, "manual:rollback", f.now, nil, "", ""); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	if len(f.receipts(r.ID)) != 0 || f.get(r.ID).OccurrenceCount != 0 {
		t.Fatal("partial rollback")
	}
	first := f.manual(r.ID, "rollback")
	if first.Number != 1 {
		t.Fatalf("rollback key lost %+v", first)
	}
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, first.NodeID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, f.parent)
		return err
	})
	skipped := f.manual(r.ID, "deleted")
	if skipped.Reason != "target_unavailable" || skipped.NodeID != nil {
		t.Fatalf("invalid target %+v", skipped)
	}
}

func TestQueueFailureRollsBackOccurrenceAndAudit(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.QueueEach = true
	r := f.create(in)
	// A tenant kind rule refuses the work-order child after the ticket has been
	// inserted. The entire composite occurrence, receipt and audit must roll back.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=ARRAY['work']::text[] WHERE slug='work'`)
		return err
	})
	f.call(f.p, "POST", "/api/recurrences/"+r.ID+"/run-now", map[string]string{"idempotency_key": "queue-failed"}, 400)
	if len(f.receipts(r.ID)) != 0 || f.get(r.ID).OccurrenceCount != 0 {
		t.Fatal("queue failure left receipt")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type IN ('node.created','recurrence.occurred','queue.added') AND metadata->>'recurrence_id'=$1`, r.ID).Scan(&count)
		if count != 0 {
			t.Fatalf("queue failure left %d events", count)
		}
		return err
	})
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=NULL WHERE slug='work'`)
		return err
	})
	if got := f.manual(r.ID, "queue-failed"); got.Outcome != "created" || got.Number != 1 {
		t.Fatalf("retry %+v", got)
	}
}
func TestWriteAuthorizationAfterConcurrentDemotion(t *testing.T) {
	f := setup(t)
	r := f.create(f.input())
	var memberID string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Member') RETURNING id::text`, f.p.TenantID).Scan(&memberID)
	})
	dbtest.BindRole(t, f.d, f.p.TenantID, memberID, "member")
	member := tenant.Principal{ID: memberID, TenantID: f.p.TenantID, Kind: tenant.Person}
	tx, err := f.d.App.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("POST", "/api/recurrences/"+r.ID+"/pause", strings.NewReader(`{"expected_revision":1}`))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), member))
		out := httptest.NewRecorder()
		close(started)
		f.handler.ServeHTTP(out, req)
		done <- out
	}()
	<-started
	if _, err = tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1 AND scope_type='workspace'`, memberID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out.Code != 403 {
		t.Fatalf("demoted write %d %s", out.Code, out.Body.String())
	}
	if f.get(r.ID).Paused {
		t.Fatal("demoted caller changed state")
	}
}
func TestDefinitionEditsPreserveDueCursorAndPausedManualRun(t *testing.T) {
	f := setup(t)
	r := f.create(f.input())
	original := *r.NextAt
	f.now = f.now.Add(10 * 24 * time.Hour)
	input := r.Input
	input.Trigger.StartDate = ""
	input.Template.Description = "Updated criteria context"
	raw, _ := json.Marshal(input)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	body["expected_revision"] = r.Revision
	f.call(f.p, "PUT", "/api/recurrences/"+r.ID, body, 200)
	current := f.get(r.ID)
	if !current.NextAt.Equal(original) {
		t.Fatal("template edit discarded overdue work")
	}
	f.call(f.p, "PUT", "/api/recurrences/"+r.ID, body, 409)
	f.call(f.p, "POST", "/api/recurrences/"+r.ID+"/pause", map[string]int64{"expected_revision": current.Revision}, 200)
	paused := f.get(r.ID)
	manual := f.manual(r.ID, "paused-manual")
	if manual.Outcome != "created" {
		t.Fatalf("paused manual %+v", manual)
	}
	after := f.get(r.ID)
	if !after.Paused || !after.NextAt.Equal(*paused.NextAt) || after.EventCursor != paused.EventCursor {
		t.Fatal("manual run moved schedule")
	}
}

// Signal at the exact tree-lock statement. On the old implementation the
// recurrence already held the tenant row here; existing writers held the tree
// and then needed that row. This barrier deterministically creates that cycle.
type treeBarrierTx struct {
	pgx.Tx
	attempted chan struct{}
	once      sync.Once
}

func (tx *treeBarrierTx) signal(sql string) {
	if strings.Contains(sql, "pg_advisory_xact_lock") {
		tx.once.Do(func() { close(tx.attempted) })
	}
}
func (tx *treeBarrierTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.signal(sql)
	return tx.Tx.Exec(ctx, sql, args...)
}
func (tx *treeBarrierTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.signal(sql)
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func TestRecurrenceWriteLockOrderWithTreeWriters(t *testing.T) {
	for _, writer := range []string{"node write", "membership change"} {
		t.Run(writer, func(t *testing.T) {
			f := setup(t)
			r := f.create(f.input())
			caller := f.p
			if writer == "membership change" {
				f.tx(func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Concurrent member') RETURNING id::text`, f.p.TenantID).Scan(&caller.ID)
				})
				dbtest.BindRole(t, f.d, f.p.TenantID, caller.ID, "member")
			}
			ctx, cancel := context.WithTimeout(dbtest.Seed(t.Context()), 10*time.Second)
			defer cancel()
			other, err := f.d.App.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Rollback(t.Context())
			if _, err = other.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, f.p.TenantID); err != nil {
				t.Fatal(err)
			}
			if _, err = other.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, f.p.TenantID); err != nil {
				t.Fatal(err)
			}
			attempted := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- db.InTenant(ctx, f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
					barrier := &treeBarrierTx{Tx: tx, attempted: attempted}
					if _, err := lock(ctx, barrier, f.p.TenantID, false); err != nil {
						return err
					}
					if err := manage(ctx, tx, caller, f.project); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `UPDATE recurrences SET paused=true WHERE id=$1`, r.ID)
					return err
				})
			}()
			select {
			case <-attempted:
			case err := <-done:
				t.Fatalf("recurrence failed before tree barrier: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if writer == "node write" {
				// The normal node INSERT takes a tenant FK key-share lock.
				_, err = other.Exec(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,position)
 SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Concurrent node',$2,4096 FROM node_kinds k WHERE slug='work'`, f.p.TenantID, f.parent)
			} else {
				// Match lockProjectMutation: tree first, then tenant FOR UPDATE.
				_, err = other.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, f.p.TenantID)
				if err == nil {
					_, err = other.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1 AND scope_type='workspace'`, caller.ID)
				}
			}
			if err == nil {
				err = other.Commit(ctx)
			} else {
				_ = other.Rollback(t.Context())
			}
			recurrenceErr := <-done
			if err != nil {
				t.Fatalf("%s error %v; recurrence error %v", writer, err, recurrenceErr)
			}
			if writer == "membership change" {
				if !errors.Is(recurrenceErr, authz.ErrForbidden) || f.get(r.ID).Paused {
					t.Fatalf("demoted recurrence write was not denied: %v", recurrenceErr)
				}
			} else if recurrenceErr != nil || !f.get(r.ID).Paused {
				t.Fatalf("recurrence write did not commit: %v", recurrenceErr)
			}
		})
	}
}

func TestPersistentDueFailureDoesNotStarveTenant(t *testing.T) {
	for _, failure := range []string{"invalid time cursor", "invalid publication", "queue rollback"} {
		t.Run(failure, func(t *testing.T) {
			f := setup(t)
			in := f.input()
			in.OverlapPolicy = "create"
			if failure == "invalid publication" {
				in.Trigger = Trigger{Kind: "event", Event: "release.published"}
			}
			in.QueueEach = failure == "queue rollback"
			bad := f.create(in)
			in.QueueEach = false
			if failure != "invalid publication" {
				in.Trigger.TimeOfDay = "10:00"
			}
			if failure == "invalid publication" {
				in.ProjectID = f.node("project", nil, "Other project")
				in.ParentID = f.node("work", &in.ProjectID, "Other parent")
			}
			good := f.create(in)
			f.now = timestamp(t, "2026-10-19T12:00:00Z")
			f.tx(func(tx pgx.Tx) error {
				if failure == "invalid publication" {
					if _, err := tx.Exec(t.Context(), `SELECT id FROM nodes WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, []string{bad.ProjectID, good.ProjectID}); err != nil {
						return err
					}
					// Force deterministic claim ordering for the NULL next_at rows.
					if _, err := tx.Exec(t.Context(), `UPDATE recurrences SET id=$2 WHERE id=$1`, bad.ID, "10000000-0000-4000-8000-000000000010"); err != nil {
						return err
					}
					bad.ID = "10000000-0000-4000-8000-000000000010"
					if _, err := tx.Exec(t.Context(), `UPDATE recurrences SET id=$2 WHERE id=$1`, good.ID, "10000000-0000-4000-8000-000000000020"); err != nil {
						return err
					}
					good.ID = "10000000-0000-4000-8000-000000000020"
					if _, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &bad.ProjectID, Type: "release.published", After: "invalid publication"}); err != nil {
						return err
					}
					_, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &good.ProjectID, Type: "release.published", After: Publication{ProjectID: good.ProjectID, PublishedAt: f.now, Name: "Release", Version: "261019120000.0.0"}})
					return err
				}
				badAt := *bad.NextAt
				if failure == "invalid time cursor" {
					badAt = badAt.Add(time.Minute)
				}
				if _, err := tx.Exec(t.Context(), `UPDATE recurrences SET next_at=$2 WHERE id=$1`, bad.ID, badAt); err != nil {
					return err
				}
				if failure == "invalid time cursor" {
					// Latest slot is 09:00 Vienna, but the corrupt cursor is 09:01.
					if _, err := tx.Exec(t.Context(), `UPDATE recurrences SET next_at=$2 WHERE id=$1`, bad.ID, timestamp(t, "2026-10-19T07:01:00Z")); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(t.Context(), `UPDATE recurrences SET next_at=$2 WHERE id=$1`, good.ID, timestamp(t, "2026-10-19T08:00:00Z")); err != nil {
					return err
				}
				if failure == "queue rollback" {
					_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=ARRAY['work']::text[] WHERE slug='work'`)
					return err
				}
				return nil
			})
			// Both passes see the persistent bad row; the first must still run
			// the later good row, and the second must not duplicate its receipt.
			for pass := 0; pass < 2; pass++ {
				err := f.m.RunTenant(t.Context(), f.p.TenantID)
				if got := f.receipts(good.ID); len(got) != 1 || got[0].Outcome != "created" {
					t.Fatalf("pass %d starved good row: %+v", pass, got)
				}
				if err == nil || !strings.Contains(err.Error(), bad.ID) {
					t.Fatalf("pass %d did not report bad row: %v", pass, err)
				}
				if len(f.receipts(bad.ID)) != 0 || f.get(bad.ID).OccurrenceCount != 0 {
					t.Fatal("failure left a partial occurrence")
				}
			}
			f.tx(func(tx pgx.Tx) error {
				var count int
				err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='recurrence.failed' AND after->>'recurrence_id'=$1`, bad.ID).Scan(&count)
				if count != 2 {
					t.Fatalf("failure audit count %d want 2", count)
				}
				return err
			})
		})
	}
}

func TestRecurrenceFenceAllowsTenantKeyShare(t *testing.T) {
	f := setup(t)
	tx, err := f.d.App.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true)`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = lock(t.Context(), tx, f.p.TenantID, false); err != nil {
		t.Fatal(err)
	}
	// NOWAIT proves compatibility directly, without a timing assertion.
	if err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(other pgx.Tx) error {
		_, err := other.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR KEY SHARE NOWAIT`, f.p.TenantID)
		return err
	}); err != nil {
		t.Fatalf("recurrence fence blocks tenant FK key-share: %v", err)
	}
}
