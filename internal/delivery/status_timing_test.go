// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"errors"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestTicketStatusTimingReopenSkipBackwardAndWindows(t *testing.T) {
	// Risk: reopens collapse visits, rework counts twice, skipped gates are
	// invented, or a percentile is interpolated instead of nearest rank.
	base := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	actor := &timingActor{"Builder", "agent"}
	n := timingNode{ID: "one", Created: base, State: "accepted", Kind: "work", Priority: "high", Bug: true}
	states := []string{"new", "open", "in_progress", "qa", "in_progress", "qa", "done", "in_progress", "qa", "done", "delivered", "accepted"}
	hours := []int{0, 2, 3, 5, 6, 8, 9, 10, 11, 12, 15, 18}
	es := []timingEvent{}
	for i, state := range states {
		e := timingEvent{ID: int64(i + 1), Type: "node.updated", At: base.Add(time.Duration(hours[i]) * time.Hour), After: state, Actor: actor}
		if i == 0 {
			e.Type = "node.created"
		} else {
			e.Before = states[i-1]
		}
		es = append(es, e)
	}
	now := base.Add(20 * time.Hour)
	l := deriveLifeline(n, es, now)
	if !l.Complete || len(l.Visits) != len(states) || !l.Visits[7].Again || !l.Visits[8].Again || l.Visits[11].Seconds != 0 {
		t.Fatalf("visits: %+v", l)
	}
	var total float64
	for _, v := range l.Visits {
		total += v.Seconds
	}
	if total != 18*3600 {
		t.Fatalf("lifeline total %v", total)
	}
	want := map[string]float64{"created_to_live": 15 * 3600, "new_to_picked_up": 3 * 3600, "in_progress": 5 * 3600, "qa_to_merged": 3 * 3600, "done_to_live": 4 * 3600, "live_to_accepted": 3 * 3600}
	for _, s := range timingSamples(l, "delivered") {
		if want[s.Step] != s.Secs {
			t.Fatalf("sample %+v, want %v", s, want[s.Step])
		}
		delete(want, s.Step)
	}
	if len(want) != 0 {
		t.Fatalf("missing samples %+v", want)
	}
	out := aggregateTiming("project", []timingNode{n}, map[string][]timingEvent{n.ID: es}, timingFilter{Days: 7, Type: "bug", Priority: "high", WorkedBy: "agent"}, now)
	if out.Rework != 1 || out.Coverage.Complete != 1 || out.FinalStatus != "delivered" || out.Steps[0].N != 1 || *out.Steps[0].P50 != 15*3600 {
		t.Fatalf("aggregate %+v", out)
	}
	if out.Buckets[0].State != "no_data" || out.Buckets[len(out.Buckets)-1].State != "partial" {
		t.Fatalf("coverage buckets %+v", out.Buckets)
	}
	none := aggregateTiming("project", []timingNode{n}, map[string][]timingEvent{n.ID: es}, timingFilter{Days: 7, WorkedBy: "person"}, now)
	if none.Coverage.Tickets != 0 || none.Steps[0].P50 != nil {
		t.Fatalf("worker filter %+v", none)
	}
	a, b := timingQuantiles([]float64{4, 1, 3, 2})
	if *a != 2 || *b != 4 {
		t.Fatalf("nearest rank p50=%v p90=%v", *a, *b)
	}
	a, b = timingQuantiles([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	if *a != 5 || *b != 9 {
		t.Fatalf("nearest rank ten p50=%v p90=%v", *a, *b)
	}
	skip := timingNode{ID: "skip", Created: base, State: "delivered", Kind: "work"}
	skipped := deriveLifeline(skip, []timingEvent{{ID: 1, Type: "node.created", At: base, After: "new"}, {ID: 2, Type: "node.updated", At: base.Add(time.Hour), Before: "new", After: "delivered"}}, now)
	ss := timingSamples(skipped, "delivered")
	if len(ss) != 1 || ss[0].Step != "created_to_live" || ss[0].Secs != 3600 {
		t.Fatalf("invented skipped steps %+v", ss)
	}
	// A completed step belongs to the prior window when its end is before the
	// current lower bound, even if its creation is much earlier.
	prior := aggregateTiming("project", []timingNode{n}, map[string][]timingEvent{n.ID: es}, timingFilter{Days: 7}, now.AddDate(0, 0, 8))
	if prior.Steps[0].N != 0 || prior.Steps[0].PrevN != 1 || *prior.Steps[0].PrevP50 != 15*3600 {
		t.Fatalf("prior window %+v", prior.Steps[0])
	}
}

func TestTicketStatusTimingSourceReplayInferenceAndGaps(t *testing.T) {
	// Risk: ingestion timestamps replace source timestamps; absent or broken
	// history enters medians; an open visit keeps accruing after completion.
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	n := timingNode{ID: "imported", State: "in_progress", Created: base}
	es := []timingEvent{
		{ID: 10, Type: "import.history", At: base.Add(time.Second), After: "new"},
		{ID: 11, Type: "import.history", At: base.Add(time.Hour), After: "in-progress"},
		{ID: 1, Type: "import.node_created", At: base.Add(24 * time.Hour), After: "in_progress"},
	}
	l := deriveLifeline(n, es, base.Add(48*time.Hour))
	if !l.Complete || l.InitialOffsetSeconds != 1 || len(l.Visits) != 2 || l.Visits[0].Seconds != 3600 || !l.Visits[1].Ongoing || l.Visits[1].Seconds != 47*3600 || l.Visits[1].To != nil {
		t.Fatalf("source replay %+v", l)
	}
	n.State = "done"
	gap := deriveLifeline(n, append(es, timingEvent{ID: 12, Type: "node.updated", At: base.Add(30 * time.Hour), Before: "open", After: "done"}), base.Add(48*time.Hour))
	if gap.Complete || gap.GapCount != 1 || len(timingSamples(gap, "done")) != 0 {
		t.Fatalf("gap accepted %+v", gap)
	}
	missing := deriveLifeline(n, nil, base.Add(48*time.Hour))
	if missing.Complete || !missing.Inferred || missing.Visits[0].MovedBy != nil || len(timingSamples(missing, "done")) != 0 {
		t.Fatalf("missing history %+v", missing)
	}
	if !reflect.DeepEqual(missing, deriveLifeline(n, nil, base.Add(48*time.Hour))) {
		t.Fatal("virtual fallback is not idempotent")
	}
	late := deriveLifeline(n, []timingEvent{{ID: 1, Type: "import.history", At: base.Add(time.Hour), After: "done"}}, base.Add(48*time.Hour))
	if late.Complete || !late.Inferred || late.InitialOffsetSeconds != 3600 {
		t.Fatalf("undisclosed initial gap %+v", late)
	}
}

func appendTimingFixture(t *testing.T, f *fixture, node string, at time.Time, typ string, before, after any) {
	t.Helper()
	f.tx(t, func(tx pgx.Tx) error {
		_, err := events.Append(t.Context(), tx, f.person, events.Change{NodeID: &node, Type: typ, Before: before, After: after, At: &at})
		return err
	})
}

func TestTicketStatusTimingBackfillIsIdempotentAndTenantScoped(t *testing.T) {
	// Risk: backfill duplicates baselines, invents actors, changes status, leaks
	// another tenant/project or writes after current permission was removed.
	f := newFixture(t)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET created_at=$2,state='open' WHERE id=$1`, f.ticket, f.at.Add(-2*time.Hour))
		return err
	})
	appendTimingFixture(t, f, f.ticket, f.at.Add(-time.Hour), "harness.usage_reported", nil, map[string]any{"state": "done", "body": "private telemetry sentinel"})
	path := "/api/projects/" + f.project + "/delivery/tickets/backfill"
	var result timingBackfillResult
	f.call(t, f.agent, "POST", path, map[string]any{}, 403, nil)
	f.call(t, f.foreign, "POST", path, map[string]any{}, 404, nil)
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &result)
	if result.Recorded != 1 || result.Next != nil {
		t.Fatalf("backfill %+v", result)
	}
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &result)
	if result.Recorded != 0 {
		t.Fatalf("duplicate backfill %+v", result)
	}
	// A different project-scoped editor must see the previous recorder's
	// evidence through event RLS; actor-only visibility would duplicate it.
	var editor, role string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Timing editor') RETURNING id::text`, f.person.TenantID).Scan(&editor); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'timing_editor','Timing editor') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read'),($1,$2,'nodes.write'),($1,$2,'events.read')`, f.person.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.person.TenantID, editor, role, f.project)
		return err
	})
	who := tenant.Principal{ID: editor, TenantID: f.person.TenantID, Kind: tenant.Person}
	f.call(t, who, "POST", path, map[string]any{}, 200, &result)
	if result.Recorded != 0 {
		t.Fatalf("another project editor duplicated backfill: %+v", result)
	}
	var count int
	var state string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT n.state,(SELECT count(*) FROM events WHERE node_id=n.id AND type='node.status_history_backfilled') FROM nodes n WHERE id=$1`, f.ticket).Scan(&state, &count)
	})
	if count != 1 || state != "open" {
		t.Fatalf("backfill mutated status or duplicated evidence: %s/%d", state, count)
	}
	var life ticketLifeline
	f.call(t, f.person, "GET", "/api/nodes/"+f.ticket+"/lifeline", nil, 200, &life)
	if !life.Inferred || life.Complete || len(life.Visits) != 1 || life.Visits[0].Source != "inferred" || life.Visits[0].MovedBy != nil || life.Visits[0].Seconds != 7200 {
		t.Fatalf("inferred life %+v", life)
	}
	f.call(t, who, "GET", "/api/nodes/"+f.ticket+"/lifeline", nil, 200, &life)
	if !life.Inferred || life.GapCount != 0 {
		t.Fatalf("project reader lost baseline provenance: %+v", life)
	}
	f.call(t, f.foreign, "GET", "/api/nodes/"+f.ticket+"/lifeline", nil, 404, nil)
	f.call(t, f.agent, "GET", "/api/nodes/"+f.ticket+"/lifeline", nil, 403, nil)
	var timing ticketTiming
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/tickets", nil, 200, &timing)
	if timing.Coverage.Inferred != 1 || timing.Coverage.Complete != 0 || timing.Steps[0].N != 0 {
		t.Fatalf("inferred timing %+v", timing)
	}
	// Explicit project visibility is applied by node RLS inside the snapshot.
	r := httptest.NewRequest("GET", "/api/nodes/"+f.ticket+"/lifeline", nil)
	r = r.WithContext(db.NoProjects(tenant.WithPrincipal(r.Context(), f.person), "timing RLS test"))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("hidden node returned %d: %s", w.Code, w.Body.String())
	}
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/tickets?window=400", nil, 400, nil)
	f.call(t, f.person, "POST", path, map[string]any{"limit": 201}, 400, nil)
}

func TestTicketStatusTimingImportedHistoryAndQuantilesAPI(t *testing.T) {
	// Risk: the real imported shape cannot be read under RLS, parent estimates
	// count twice, or status-time samples use PR telemetry instead of node events.
	f := newFixture(t)
	base := f.at.Add(-10 * time.Hour)
	var imported, parent, child string
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET created_at=$2,state='accepted' WHERE id=$1`, f.ticket, base); err != nil {
			return err
		}
		for i, target := range []*string{&imported, &parent} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title,state,created_at) SELECT $1,$2,$3,id,'Status timing','done',$4 FROM node_kinds WHERE slug='work' RETURNING id::text`, f.person.TenantID, f.project, []string{"AEON-9001", "AEON-9002"}[i], base).Scan(target); err != nil {
				return err
			}
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title,state,created_at) SELECT $1,$2,'AEON-9003',id,'Child','new',$3 FROM node_kinds WHERE slug='work' RETURNING id::text`, f.person.TenantID, parent, base).Scan(&child)
	})
	states := []string{"new", "in_progress", "qa", "done", "delivered", "accepted"}
	hours := []int{0, 2, 3, 4, 7, 8}
	for i, s := range states {
		typ := "node.updated"
		var before any
		if i == 0 {
			typ = "node.created"
		} else {
			before = map[string]any{"state": states[i-1]}
		}
		appendTimingFixture(t, f, f.ticket, base.Add(time.Duration(hours[i])*time.Hour), typ, before, map[string]any{"state": s})
	}
	for i, s := range []string{"new", "in-progress", "qa", "done"} {
		at := base.Add(time.Duration([]int{0, 2, 4, 5}[i]) * time.Hour)
		if i == 0 {
			at = at.Add(time.Second)
		}
		appendTimingFixture(t, f, imported, at, "import.history", nil, map[string]any{"classic_ref": "fixture:import.history:" + strconv.Itoa(i), "record": map[string]any{"snapshot": map[string]any{"status": s}, "changed_by": 7, "changed_by_name": "Historical builder"}})
	}
	appendTimingFixture(t, f, imported, base.Add(9*time.Hour), "import.node_created", nil, map[string]any{"state": "done"})
	appendTimingFixture(t, f, parent, base, "node.created", nil, map[string]any{"state": "done"})
	appendTimingFixture(t, f, child, base, "node.created", nil, map[string]any{"state": "new"})
	var out ticketTiming
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/tickets?window=7", nil, 200, &out)
	if out.Coverage.Tickets != 3 || out.Coverage.Complete != 3 || out.FinalStatus != "delivered" || out.Steps[0].N != 1 || *out.Steps[0].P50 != 7*3600 {
		t.Fatalf("API aggregate %+v", out)
	}
	if out.Steps[2].N != 2 || *out.Steps[2].P50 != 3600 || *out.Steps[2].P90 != 7200 {
		t.Fatalf("API quantiles %+v", out.Steps[2])
	}
	var life ticketLifeline
	f.call(t, f.person, "GET", "/api/nodes/"+imported+"/lifeline", nil, 200, &life)
	if !life.Complete || life.InitialOffsetSeconds != 1 || len(life.Visits) != 4 || life.Visits[0].Seconds != 7200 || life.Visits[1].Status != "in_progress" || life.Visits[1].MovedBy.Name != "Historical builder" {
		t.Fatalf("imported API life %+v", life)
	}
	// Directly exercise the bounded reader without creating 2001 fixture rows.
	err := db.InTenantReadSnapshot(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		_, err := loadTimingEvents(t.Context(), tx, []timingNode{{ID: f.ticket}}, f.at, 1)
		return err
	})
	var bounded *apiError
	if !errors.As(err, &bounded) || bounded.status != 503 {
		t.Fatalf("event budget error: %v", err)
	}
}
