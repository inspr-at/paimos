// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

func alertObservation(f *fixture) Observation {
	pr := int64(7)
	return Observation{ID: stableID(f.person.TenantID, f.m.config.Repository, subject(&pr, nil)),
		Project: &f.project, Ticket: &f.ticket, Repository: f.m.config.Repository, PR: &pr,
		Head: strings.Repeat("b", 40), Open: true, ReviewedHead: strings.Repeat("a", 40), Settings: defaults(), At: f.at}
}

func saveAlertObservation(t *testing.T, f *fixture, o Observation) Item {
	t.Helper()
	var item Item
	f.tx(t, func(tx pgx.Tx) error {
		if err := db.LockTree(t.Context(), tx, f.person.TenantID); err != nil {
			return err
		}
		var err error
		item, _, err = saveTx(t.Context(), tx, f.person.TenantID, o)
		return err
	})
	return item
}

func setAlertLead(t *testing.T, f *fixture) {
	t.Helper()
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,updated_by)
 VALUES($1,$2,$3,$3)`, f.person.TenantID, f.project, f.person.ID)
		return err
	})
}

func alertCounts(t *testing.T, f *fixture) (int, int, int) {
	t.Helper()
	var alerts, messages, events int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery_alerts),
 (SELECT count(*) FROM inbox_messages WHERE sender_label='Delivery'),
 (SELECT count(*) FROM events WHERE type='delivery.stall_alerted' AND node_id=$1)`, f.ticket).Scan(&alerts, &messages, &events)
	})
	return alerts, messages, events
}

func sweepAlerts(t *testing.T, f *fixture, want int) {
	t.Helper()
	n, err := f.m.SweepAlerts(t.Context(), f.person.TenantID)
	if err != nil || n != want {
		t.Fatalf("alert sweep: count %d, want %d, error %v", n, want, err)
	}
}

func TestDeliveryAlertsEpisodesIsolationAndSettings(t *testing.T) {
	// Risk: duplicate durable messages across retries/restarts, silent loss of
	// stalled episodes, stale-state alerts, and tenant/project information leaks.
	// One injected clock proves the exact deadline boundary without sleeps.
	f := newFixture(t)
	setAlertLead(t, f)
	var settings Settings
	f.call(t, f.person, "PUT", "/api/settings/delivery", map[string]any{"deadlines": map[string]int{"reviewed": 2, "pushed": 3}}, 200, &settings)
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/delivery-settings", map[string]any{"deadlines": map[string]int{"reviewed": 1}}, 200, &settings)
	o := alertObservation(f)
	i := saveAlertObservation(t, f, o)
	if i.State != Reviewed {
		t.Fatal("fixture did not establish reviewed state")
	}
	f.at = o.At.Add(45 * time.Second)
	if delay, err := f.m.nextAlertDelay(t.Context(), f.person.TenantID); err != nil || delay != 15*time.Second {
		t.Fatalf("effective project deadline did not drive timer: %v, %v", delay, err)
	}
	f.at = o.At.Add(time.Minute)
	sweepAlerts(t, f, 0)
	f.at = f.at.Add(time.Microsecond)
	sweepAlerts(t, f, 1)
	sweepAlerts(t, f, 0)
	// A new module has no in-memory episode state. Durable SQL must dedupe it.
	f.m = New(f.d.App, f.m.config, nil, nil, nil)
	f.m.now = func() time.Time { return f.at }
	sweepAlerts(t, f, 0)
	if a, m, e := alertCounts(t, f); a != 1 || m != 1 || e != 1 {
		t.Fatalf("restart duplicated/lost acceptance: alerts %d, messages %d, events %d", a, m, e)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var body, key, sender, recipient string
		if err := tx.QueryRow(t.Context(), `SELECT body,idempotency_key,sender_principal_id::text,recipient_principal_id::text FROM inbox_messages WHERE sender_label='Delivery'`).Scan(&body, &key, &sender, &recipient); err != nil {
			return err
		}
		if key != alertKey(f.person.TenantID, i) || recipient != f.person.ID || strings.ContainsAny(body, "\r\n") || !strings.Contains(body, "AEON-848 · PR #7: reviewed for 1 min") || !strings.Contains(body, "next: coordinator") || !strings.Contains(body, "1 min)") {
			t.Fatalf("wrong durable message: %q, key %q, recipient %s", body, key, recipient)
		}
		var system bool
		if err := tx.QueryRow(t.Context(), `SELECT name='System' AND roles @> ARRAY['system'] FROM principals WHERE id=$1`, sender).Scan(&system); err != nil {
			return err
		}
		if !system {
			t.Fatal("notice did not use the system actor")
		}
		return nil
	})
	var page AlertPage
	f.call(t, f.person, "GET", "/api/delivery/alerts?project=AEON&open=true", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].MessageID == nil || page.Items[0].ClearedAt != nil {
		t.Fatal("open alert not visible with durable message identity")
	}
	// Change after alert: clear synchronously, without a second message.
	f.at = f.at.Add(time.Second)
	o.ReviewedHead, o.At = o.Head, f.at
	i = saveAlertObservation(t, f, o)
	if i.State != Pushed {
		t.Fatal("fixture did not move to pushed")
	}
	f.call(t, f.person, "GET", "/api/delivery/alerts?open=true", nil, 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("state transition did not atomically clear alert")
	}
	// Missing project duration inherits tenant's pushed duration, rather than
	// the observation's old default (60 minutes).
	f.at = i.Since.Add(3*time.Minute + time.Microsecond)
	sweepAlerts(t, f, 1)
	if a, m, e := alertCounts(t, f); a != 2 || m != 2 || e != 2 {
		t.Fatalf("tenant duration did not apply: %d %d %d", a, m, e)
	}
	f.call(t, f.person, "GET", "/api/delivery/alerts?limit=1", nil, 200, &page)
	if len(page.Items) != 1 || page.Next == nil {
		t.Fatal("episode keyset did not expose continuation")
	}
	first := page.Items[0]
	f.call(t, f.person, "GET", "/api/delivery/alerts?limit=1&after="+url.QueryEscape(*page.Next), nil, 200, &page)
	if len(page.Items) != 1 || page.Next != nil || page.Items[0].State == first.State {
		t.Fatal("episode keyset skipped/repeated a row")
	}
	f.call(t, f.foreign, "GET", "/api/delivery/alerts", nil, 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("foreign tenant saw alert")
	}
	if err := db.InTenant(db.AllProjects(t.Context(), "alert tenant RLS test"), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_alerts WHERE tenant_id=$1`, f.person.TenantID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("direct alert RLS leaked across tenants")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Same tenant, no project visibility, using the application role directly.
	if err := db.InTenant(db.OnlyProjects(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_alerts`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("alert project policy leaked")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"open=bad", "limit=101", "after=bad&limit=1"} {
		f.call(t, f.person, "GET", "/api/delivery/alerts?"+query, nil, 400, nil)
	}
	// State change before the next deadline must never produce a message.
	f.at = f.at.Add(time.Second)
	o.Head, o.At = strings.Repeat("c", 40), f.at
	saveAlertObservation(t, f, o)
	f.at = f.at.Add(30 * time.Second)
	o.Merged, o.At = true, f.at
	saveAlertObservation(t, f, o)
	f.at = f.at.Add(time.Hour)
	sweepAlerts(t, f, 0)
	if a, m, e := alertCounts(t, f); a != 2 || m != 2 || e != 2 {
		t.Fatalf("pre-deadline state change sent a message: %d %d %d", a, m, e)
	}
	if delay, err := f.m.nextAlertDelay(t.Context(), f.person.TenantID); err != nil || delay != time.Minute {
		t.Fatalf("terminal/already alerted item caused busy timer: %v, %v", delay, err)
	}
}

func TestDeliveryAlertsAcceptanceRollbackAndLeadAuthority(t *testing.T) {
	// Risk: partial inbox failure burns the idempotency episode, or a revoked
	// lead receives project details from a service with global visibility.
	f := newFixture(t)
	setAlertLead(t, f)
	o := alertObservation(f)
	saveAlertObservation(t, f, o)
	f.at = f.at.Add(31 * time.Minute)
	// Fail exactly the durable acceptance, after its sent event was appended.
	if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION fail_delivery_notice() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.sender_label='Delivery' THEN RAISE EXCEPTION 'injected delivery inbox failure'; END IF; RETURN NEW; END; $$;
 CREATE TRIGGER fail_delivery_notice BEFORE INSERT ON inbox_messages FOR EACH ROW EXECUTE FUNCTION fail_delivery_notice()`); err != nil {
		t.Fatal(err)
	}
	if n, err := f.m.SweepAlerts(t.Context(), f.person.TenantID); err == nil || n != 0 || !strings.Contains(err.Error(), "injected delivery inbox failure") {
		t.Fatalf("wrong acceptance failure: count %d, error %v", n, err)
	}
	if a, m, e := alertCounts(t, f); a != 0 || m != 0 || e != 0 {
		t.Fatalf("partial failure was committed: %d %d %d", a, m, e)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='inbox.sent'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("failed acceptance retained inbox.sent event")
		}
		return nil
	})
	if _, err := f.d.Admin.Exec(t.Context(), `DROP TRIGGER fail_delivery_notice ON inbox_messages`); err != nil {
		t.Fatal(err)
	}
	sweepAlerts(t, f, 1)
	// No lead: event only, durably idempotent.
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM project_lead_settings WHERE project_id=$1`, f.project)
		return err
	})
	o.Head, o.At = strings.Repeat("c", 40), f.at
	saveAlertObservation(t, f, o)
	f.at = f.at.Add(31 * time.Minute)
	sweepAlerts(t, f, 1)
	sweepAlerts(t, f, 0)
	if a, m, e := alertCounts(t, f); a != 2 || m != 1 || e != 2 {
		t.Fatalf("no-lead path is not event-only: %d %d %d", a, m, e)
	}
	setAlertLead(t, f)
	// Preserve the configured owner but revoke its bindings under the same
	// tenant access fence used by the final alert mutation.
	f.tx(t, func(tx pgx.Tx) error {
		if err := db.LockTenant(t.Context(), tx, f.person.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
		return err
	})
	o.Head, o.At = strings.Repeat("d", 40), f.at
	saveAlertObservation(t, f, o)
	f.at = f.at.Add(31 * time.Minute)
	sweepAlerts(t, f, 1)
	if a, m, e := alertCounts(t, f); a != 3 || m != 1 || e != 3 {
		t.Fatalf("revoked lead received delivery details: %d %d %d", a, m, e)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_alerts WHERE recipient_principal_id IS NULL AND inbox_message_id IS NULL`).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			t.Fatal("event-only alert falsely recorded delivery")
		}
		return nil
	})
	// Shutdown needs no timer wake, including when GitHub is unconfigured.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.m.RunAlerts(ctx)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/delivery/alerts", nil))
	if w.Code != 401 {
		t.Fatal("unauthenticated alert list was not refused")
	}
}
