// SPDX-License-Identifier: AGPL-3.0-only

package hours

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func (f *fixture) correction(p tenant.Principal, method string, e Entry, body any, headers ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "/api/time-entries/"+e.ID, strings.NewReader(string(raw)))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	for i := 0; i < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
func (f *fixture) entry(period Period) Entry {
	f.t.Helper()
	w := f.call(f.member, "POST", "/time-entries", f.manual(period, "EUR"))
	requireStatus(f.t, w, 201)
	return decode[Entry](f.t, w)
}
func (f *fixture) lastEvent(typ string) events.Event {
	f.t.Helper()
	var e events.Event
	f.sql(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `SELECT id,actor_principal_id::text,type,before,after FROM events WHERE type=$1 ORDER BY id DESC LIMIT 1`, typ).Scan(&e.ID, &e.ActorPrincipalID, &e.Type, &e.Before, &e.After)
	})
	return e
}
func (f *fixture) undo(p tenant.Principal, e events.Event) *httptest.ResponseRecorder {
	return f.call(p, "POST", fmt.Sprintf("/events/%d/undo", e.ID), nil)
}
func (f *fixture) totalsNow() Totals {
	w := f.call(f.admin, "GET", "/nodes/"+f.root+"/time-totals", nil)
	requireStatus(f.t, w, 200)
	return decode[Totals](f.t, w)
}
func (f *fixture) secondMember() tenant.Principal {
	p := tenant.Principal{TenantID: f.member.TenantID, Kind: tenant.Person, Roles: []string{"member"}}
	f.sql(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Other member',ARRAY['member']) RETURNING id::text`, p.TenantID).Scan(&p.ID)
	})
	dbtest.BindLegacy(f.t, f.database, p.TenantID, p.ID)
	return p
}

func TestCorrectionAuthorityPreconditionsAndApproval(t *testing.T) {
	f := setup(t)
	period := f.period(f.member)
	e := f.entry(period)
	other := f.secondMember()
	initial, digest := f.snapshot(period)
	n := f.eventCount()
	for _, method := range []string{"PATCH", "DELETE"} {
		body := map[string]any{"note": "corrected"}
		requireStatus(t, f.correction(other, method, e, body), 403)
		forged := other
		forged.Roles = []string{"admin"}
		requireStatus(t, f.correction(forged, method, e, body), 403)
		requireStatus(t, f.correction(f.other, method, e, body), 404)
		requireStatus(t, f.correction(f.customer, method, e, body), 403)
		requireStatus(t, f.correction(f.agent, method, e, body), 403)
		w := f.correction(f.member, method, e, body, "If-Match", `"stale"`)
		requireStatus(t, w, 412)
		var stale struct {
			Current Entry `json:"current"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &stale); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(comparable(stale.Current), comparable(e)) || w.Header().Get("ETag") != entryTag(e) {
			t.Fatalf("missing current snapshot: %s", w.Body.String())
		}
	}
	requireStatus(t, f.correction(f.member, "PATCH", e, map[string]any{"note": "bad"}, "If-Unmodified-Since", "invalid"), 400)
	requireStatus(t, f.correction(f.member, "PATCH", e, map[string]any{"note": "bad"}, "If-Unmodified-Since", e.UpdatedAt.Add(-time.Second).Format(time.RFC3339Nano)), 412)
	requireStatus(t, f.correction(f.member, "PATCH", e, map[string]any{"note": "bad"}, "If-Match", "W/"+entryTag(e)), 412)
	requireStatus(t, f.correction(f.member, "PATCH", e, map[string]any{"note": "bad"}, "If-Unmodified-Since", e.UpdatedAt.Format(time.RFC3339Nano), "If-Unmodified-Since", e.UpdatedAt.Format(time.RFC3339Nano)), 400)
	if f.eventCount() != n {
		t.Fatal("refused mutation wrote an event")
	}
	w := f.correction(f.member, "PATCH", e, map[string]any{"duration_seconds": 3600, "note": "corrected"}, "If-Unmodified-Since", e.UpdatedAt.Format(time.RFC3339Nano))
	requireStatus(t, w, 200)
	changed := decode[Entry](t, w)
	if changed.Amount.String() != "0.1800" || changed.DurationSeconds != 3600 || !changed.UpdatedAt.After(e.UpdatedAt) || w.Header().Get("ETag") != entryTag(changed) {
		t.Fatalf("corrected: %+v", changed)
	}
	now, hash := f.snapshot(period)
	if now.Revision != initial.Revision+1 || hash == digest || f.totalsNow().DurationSeconds != 3600 {
		t.Fatal("edit did not refresh period/totals")
	}
	requireStatus(t, f.call(f.admin, "POST", "/time-periods/"+period.ID+"/approve", approvalBody(initial, digest)), 409)
	requireStatus(t, f.correction(f.member, "PATCH", changed, map[string]any{"note": "old"}, "If-Match", entryTag(e)), 412)
	n = f.eventCount()
	requireStatus(t, f.correction(f.member, "PATCH", changed, map[string]any{"note": "corrected"}, "If-Match", entryTag(changed)), 200)
	if f.eventCount() != n {
		t.Fatal("no-op wrote an event")
	}
	// If-Match takes precedence over the malformed timestamp header.
	w = f.correction(f.admin, "PATCH", changed, map[string]any{"note": "admin correction"}, "If-Match", `"older", `+entryTag(changed), "If-Unmodified-Since", "invalid")
	requireStatus(t, w, 200)
	changed = decode[Entry](t, w)
	current, currentHash := f.snapshot(period)
	requireStatus(t, f.call(f.admin, "POST", "/time-periods/"+period.ID+"/approve", approvalBody(current, currentHash)), 200)
	n = f.eventCount()
	for _, p := range []tenant.Principal{f.member, f.admin} {
		for _, method := range []string{"PATCH", "DELETE"} {
			w = f.correction(p, method, changed, map[string]any{"note": "sealed"})
			requireStatus(t, w, 409)
			if !strings.Contains(w.Body.String(), "approved periods are immutable") {
				t.Fatal(w.Body.String())
			}
		}
	}
	if f.eventCount() != n {
		t.Fatal("approved mutation wrote an event")
	}
}

func TestCorrectionDeleteTotalsDigestUndo(t *testing.T) {
	f := setup(t)
	events.New(f.database.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	period := f.period(f.member)
	original := f.entry(period)
	initial, originalHash := f.snapshot(period)
	w := f.correction(f.member, "PATCH", original, map[string]any{"duration_seconds": 7200, "node_id": f.root, "note": "two hours"})
	requireStatus(t, w, 200)
	updated := decode[Entry](t, w)
	event := f.lastEvent("time_entry.updated")
	if event.ActorPrincipalID != f.member.ID || len(event.Before) == 0 || len(event.After) == 0 {
		t.Fatal("incomplete update event")
	}
	requireStatus(t, f.undo(f.other, event), 404)
	requireStatus(t, f.undo(f.secondMember(), event), 403)
	requireStatus(t, f.undo(f.member, event), 201)
	restored, hash := f.snapshot(period)
	if restored.Revision != initial.Revision+2 || hash != originalHash || f.totalsNow().DurationSeconds != 1 {
		t.Fatal("undo did not restore digest/totals")
	}
	requireStatus(t, f.undo(f.member, event), 409)
	entries := decode[[]Entry](t, f.call(f.member, "GET", "/time-entries", nil))
	if !entries[0].UpdatedAt.After(updated.UpdatedAt) {
		t.Fatal("undo must advance entry validator")
	}
	w = f.correction(f.member, "DELETE", entries[0], nil, "If-Match", entryTag(entries[0]))
	requireStatus(t, w, 204)
	deleted := f.lastEvent("time_entry.deleted")
	receipt := w.Header().Get(events.MutationEventHeader)
	if receipt != fmt.Sprint(deleted.ID) {
		t.Fatalf("delete receipt %q does not name its own event %d", receipt, deleted.ID)
	}
	empty, emptyHash := f.snapshot(period)
	if empty.Revision != restored.Revision+1 || emptyHash == originalHash || f.totalsNow().DurationSeconds != 0 || len(f.totalsNow().Amounts) != 0 {
		t.Fatal("delete did not update totals/digest")
	}
	w = f.correction(f.member, "DELETE", entries[0], nil)
	requireStatus(t, w, 404)
	if w.Header().Get(events.MutationEventHeader) != "" {
		t.Fatal("refused deletion supplied a receipt")
	}
	requireStatus(t, f.undo(f.admin, deleted), 201)
	_, hash = f.snapshot(period)
	if hash != originalHash || f.totalsNow().DurationSeconds != 1 {
		t.Fatal("delete undo did not restore financial snapshot")
	}
	requireStatus(t, f.undo(f.admin, deleted), 409)
	entries = decode[[]Entry](t, f.call(f.member, "GET", "/time-entries", nil))
	w = f.correction(f.admin, "DELETE", entries[0], nil, "If-Match", "*")
	requireStatus(t, w, 204)
	deleted = f.lastEvent("time_entry.deleted")
	if second := w.Header().Get(events.MutationEventHeader); second != fmt.Sprint(deleted.ID) || second == receipt {
		t.Fatalf("second deletion receipt %q, first %q", second, receipt)
	}
	requireStatus(t, f.call(f.admin, "POST", "/events/"+receipt+"/undo", nil), 409)
	if f.totalsNow().DurationSeconds != 0 {
		t.Fatal("old receipt restored the later deletion")
	}
	current, digest := f.snapshot(period)
	requireStatus(t, f.call(f.admin, "POST", "/time-periods/"+period.ID+"/approve", approvalBody(current, digest)), 200)
	requireStatus(t, f.undo(f.admin, deleted), 409)
	if f.totalsNow().DurationSeconds != 0 {
		t.Fatal("undo changed approved period")
	}
}

func TestCorrectionRatesValidationAndStaleUndo(t *testing.T) {
	f := setup(t)
	events.New(f.database.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	period := f.period(f.member)
	e := f.entry(period)
	f.sql(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE cost_unit_rates SET effective_until='2026-09-24' WHERE currency='EUR'`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO cost_unit_rates(tenant_id,cost_unit_node_id,unit,currency,internal_amount,bill_amount,effective_from,created_by_principal_id) VALUES($1,$2,'hour','EUR',0,99999999999999.9999,'2026-09-24',$3)`, f.admin.TenantID, f.cost, f.admin.ID)
		return err
	})
	for _, body := range []map[string]any{
		{}, {"note": strings.Repeat("x", 65537)}, {"principal_id": f.admin.ID}, {"duration_seconds": 1, "ended_at": "2026-09-23T12:00:03Z"},
	} {
		requireStatus(t, f.correction(f.member, "PATCH", e, body), 400)
	}
	for _, body := range []map[string]any{
		{"duration_seconds": 0}, {"duration_seconds": -1}, {"duration_seconds": int64(9223372036854775807)},
		{"started_at": "2026-08-31T23:59:59Z"}, {"ended_at": "2026-10-01T00:00:01Z"}, {"ended_at": "2026-09-23T12:00:00.5Z"},
	} {
		requireStatus(t, f.correction(f.member, "PATCH", e, body), 409)
	}
	requireStatus(t, f.correction(f.member, "PATCH", e, map[string]any{"cost_unit_node_id": f.root}), 404)
	// Date selection uses UTC and exact decimal values beyond float precision.
	w := f.correction(f.member, "PATCH", e, map[string]any{"started_at": "2026-09-23T23:00:00-02:00", "duration_seconds": 3600})
	requireStatus(t, w, 200)
	e = decode[Entry](t, w)
	if e.Amount.String() != "99999999999999.9999" || e.RateAmount.String() != e.Amount.String() {
		t.Fatal("decimal/date snapshot lost precision")
	}
	first := f.lastEvent("time_entry.updated")
	requireStatus(t, f.correction(f.member, "PATCH", e, map[string]any{"duration_seconds": 7200}), 400) // numeric overflow rolls back
	w = f.correction(f.member, "PATCH", e, map[string]any{"note": "later"})
	requireStatus(t, w, 200)
	e = decode[Entry](t, w)
	requireStatus(t, f.undo(f.member, first), 409)
	// An end-only edit and a start+end near the period boundary are supported.
	w = f.correction(f.member, "PATCH", e, map[string]any{"started_at": "2026-09-30T23:59:58Z", "ended_at": "2026-09-30T23:59:59Z"})
	requireStatus(t, w, 200)
	e = decode[Entry](t, w)
	w = f.correction(f.member, "PATCH", e, map[string]any{"ended_at": "2026-10-01T00:00:00Z"})
	requireStatus(t, w, 200)
	if decode[Entry](t, w).DurationSeconds != 2 {
		t.Fatal("end patch duration")
	}
	current, hash := f.snapshot(period)
	requireStatus(t, f.call(f.admin, "POST", "/time-periods/"+period.ID+"/approve", approvalBody(current, hash)), 200)
	requireStatus(t, f.undo(f.member, f.lastEvent("time_entry.updated")), 409)
}

func TestCorrectionAtomicityAndPluginClosure(t *testing.T) {
	f := setup(t)
	period := f.period(f.member)
	e := f.entry(period)
	before, digest := f.snapshot(period)
	n := f.eventCount()
	f.mod.writer = failingWriter{}
	for _, method := range []string{"PATCH", "DELETE"} {
		requireStatus(t, f.correction(f.member, method, e, map[string]any{"note": "must roll back"}), 500)
	}
	current, hash := f.snapshot(period)
	if current.Revision != before.Revision || hash != digest || f.eventCount() != n {
		t.Fatal("failed event persisted a change")
	}
	f.mod.writer = events.Writer{}
	events.New(f.database.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	requireStatus(t, f.correction(f.member, "PATCH", e, map[string]any{"note": "changed"}), 200)
	event := f.lastEvent("time_entry.updated")
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE plugin_installations SET enabled=false WHERE plugin_id='business_hours'`)
		return err
	})
	for _, method := range []string{"PATCH", "DELETE"} {
		requireStatus(t, f.correction(f.member, method, e, map[string]any{"note": "disabled"}), 403)
	}
	requireStatus(t, f.undo(f.member, event), 403)
}

func TestApprovalLocksOutConcurrentCorrections(t *testing.T) {
	for _, method := range []string{"PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			f := setup(t)
			period := f.period(f.member)
			e := f.entry(period)
			current, hash := f.snapshot(period)
			hook := blockingApproval{make(chan struct{}), make(chan struct{})}
			f.mod.writer = hook
			approved := make(chan *httptest.ResponseRecorder, 1)
			changed := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				approved <- f.call(f.admin, "POST", "/time-periods/"+period.ID+"/approve", approvalBody(current, hash))
			}()
			select {
			case <-hook.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("approval did not reach writer")
			}
			go func() { changed <- f.correction(f.member, method, e, map[string]any{"note": "racing"}) }()
			close(hook.release)
			requireStatus(t, <-approved, 200)
			requireStatus(t, <-changed, 409)
			sealed, _ := f.snapshot(period)
			if sealed.Approval.EntriesSHA256 != hash || sealed.Approval.TotalSeconds != 1 || f.totalsNow().DurationSeconds != 1 {
				t.Fatal("correction crossed approval")
			}
		})
	}
}

func TestComparableEntryAndLegacyDigest(t *testing.T) {
	start := time.Date(2026, 9, 23, 12, 0, 0, 123456000, time.FixedZone("local", 7200))
	e := Entry{EntryWrite: EntryWrite{StartedAt: start, EndedAt: start.Add(time.Second)}, ID: "entry", DurationSeconds: 1, RateAmount: json.Number("0.1800"), Amount: json.Number("0.0001"), UpdatedAt: start}
	raw, _ := json.Marshal(e)
	var decoded Entry
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(comparable(e), comparable(decoded)) {
		t.Fatal("equivalent timestamps compare differently")
	}
	a, _, _ := entryDigest([]Entry{e})
	// Fixed pre-B9 wire bytes pin the digest contract for historical approvals.
	legacy := []byte(`[{"period_id":"","cost_unit_node_id":"","currency":"","source":"","started_at":"2026-09-23T12:00:00.123456+02:00","ended_at":"2026-09-23T12:00:01.123456+02:00","note":"","id":"entry","duration_seconds":1,"rate_amount":0.1800,"amount":0.0001}]`)
	sum := sha256.Sum256(legacy)
	if a != hex.EncodeToString(sum[:]) {
		t.Fatal("historical digest encoding changed")
	}
	e.UpdatedAt = e.UpdatedAt.Add(time.Hour)
	b, _, _ := entryDigest([]Entry{e})
	if a != b {
		t.Fatal("validator changed billing digest")
	}
}

func TestCorrectionTenantReferences(t *testing.T) {
	f := setup(t)
	period := f.period(f.member)
	e := f.entry(period)
	var foreign string
	err := db.InTenant(dbtest.Seed(t.Context()), f.database.App, f.other.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PRJ-1','Foreign' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.other.TenantID).Scan(&foreign)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"node_id", "cost_unit_node_id"} {
		requireStatus(t, f.correction(f.member, "PATCH", e, map[string]any{field: foreign}), 404)
	}
}

func TestCorrectionCostUnitAndRunProvenance(t *testing.T) {
	f := setup(t)
	period := f.period(f.member)
	e := f.entry(period)
	var cost string
	f.sql(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,kind_id,'CU-2','Second rate' FROM nodes WHERE id=$2 RETURNING id::text`, f.admin.TenantID, f.cost).Scan(&cost); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO cost_unit_rates(tenant_id,cost_unit_node_id,unit,currency,internal_amount,bill_amount,effective_from,created_by_principal_id) VALUES($1,$2,'hour','EUR',0,123.4567,'2026-01-01',$3)`, f.admin.TenantID, cost, f.admin.ID)
		return err
	})
	w := f.correction(f.member, "PATCH", e, map[string]any{"cost_unit_node_id": cost, "duration_seconds": 3600})
	requireStatus(t, w, 200)
	e = decode[Entry](t, w)
	if e.RateAmount.String() != "123.4567" || e.Amount.String() != "123.4567" {
		t.Fatalf("new cost snapshot %+v", e)
	}
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE cost_unit_rates SET effective_until='2026-09-24' WHERE cost_unit_node_id=$1`, cost)
		return err
	})
	requireStatus(t, f.correction(f.member, "PATCH", e, map[string]any{"started_at": "2026-09-24T12:00:00Z"}), 409)
	// Merely changing the time within a date retains the pricing snapshot.
	w = f.correction(f.member, "PATCH", e, map[string]any{"started_at": "2026-09-23T13:00:00Z"})
	requireStatus(t, w, 200)
	if decode[Entry](t, w).DurationSeconds != 3600 {
		t.Fatal("start-only correction lost duration")
	}
	runPeriod := f.period(f.agent)
	w = f.call(f.admin, "POST", "/time-entries", map[string]any{"period_id": runPeriod.ID, "cost_unit_node_id": f.cost, "currency": "EUR", "source": "agent_run", "agent_run_id": f.run})
	requireStatus(t, w, 201)
	runEntry := decode[Entry](t, w)
	for _, body := range []map[string]any{{"duration_seconds": 2}, {"node_id": f.root}, {"started_at": "2026-09-23T13:00:00Z"}} {
		requireStatus(t, f.correction(f.admin, "PATCH", runEntry, body), 409)
	}
	w = f.correction(f.admin, "PATCH", runEntry, map[string]any{"note": "clarified", "cost_unit_node_id": cost})
	requireStatus(t, w, 200)
	corrected := decode[Entry](t, w)
	if corrected.DurationSeconds != 1 || *corrected.AgentRunID != f.run || corrected.PrincipalID != f.agent.ID {
		t.Fatal("run provenance changed")
	}
}

func TestConcurrentCorrectionPreconditionHasOneWinner(t *testing.T) {
	f := setup(t)
	period := f.period(f.member)
	e := f.entry(period)
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, note := range []string{"one", "two"} {
		go func() {
			results <- f.correction(f.member, "PATCH", e, map[string]any{"note": note}, "If-Match", entryTag(e))
		}()
	}
	codes := map[int]int{}
	for range 2 {
		codes[(<-results).Code]++
	}
	if codes[200] != 1 || codes[412] != 1 {
		t.Fatalf("concurrent corrections: %v", codes)
	}
}

func TestUndoUsesCurrentAdminAuthority(t *testing.T) {
	f := setup(t)
	events.New(f.database.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	period := f.period(f.member)
	e := f.entry(period)
	requireStatus(t, f.correction(f.member, "PATCH", e, map[string]any{"note": "changed"}), 200)
	event := f.lastEvent("time_entry.updated")
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1::uuid AND key='member') WHERE principal_id=$2::uuid AND scope_type='workspace'`, f.admin.TenantID, f.admin.ID)
		return err
	})
	requireStatus(t, f.undo(f.admin, event), 403)
	requireStatus(t, f.undo(f.member, event), 201)
}
