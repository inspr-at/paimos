// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/jackc/pgx/v5"
)

const reportedEvent = "agent_pairing.reported"

// readReported counts the reports a principal reads the way the events module
// does: a transaction opened for that principal, tenant rows first.
func readReported(t *testing.T, f *fixture, tenantID string, p tenant.Principal) int {
	t.Helper()
	var n int
	err := db.InTenant(tenant.WithPrincipal(t.Context(), p), f.db.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type=$1`, reportedEvent).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// listedReported reads the events history over HTTP with the current cookie.
func listedReported(f *fixture) int {
	f.t.Helper()
	var page struct {
		Items []struct {
			Type string `json:"type"`
		} `json:"items"`
	}
	decodeResult(f.t, f.call("GET", "/api/events?limit=200", nil, true, "", 200), &page)
	n := 0
	for _, e := range page.Items {
		if e.Type == reportedEvent {
			n++
		}
	}
	return n
}

// streamedReported replays the stream for a moment and counts the reports in it.
func streamedReported(f *fixture) int {
	f.t.Helper()
	server := httptest.NewServer(f.h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(f.t.Context(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/events/stream", nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.AddCookie(f.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		f.t.Fatalf("stream status %d", resp.StatusCode)
	}
	n := 0
	scan := bufio.NewScanner(resp.Body)
	for scan.Scan() {
		if strings.HasPrefix(scan.Text(), "event: "+reportedEvent) {
			n++
		}
	}
	return n
}

// AEON-434: agent_pairing.reported names a computer and its setup state, so the
// server addresses it to the one person who approved the pairing. Another
// person in the workspace, an agent key and another tenant never receive it,
// on the history list, on the stream and in a direct read alike.
func TestPairingReportReachesOnlyTheApprovingPerson(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	f.redeem(p)
	f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "progress": agentpairing.SetupProgress{State: "connected"}}, false, "", 200)
	if f.events(reportedEvent) != 1 {
		t.Fatal("daemon connect did not publish the report")
	}
	var agent string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT principal_id::text FROM agent_pairing_computers WHERE tenant_id=$1`, f.tenantID).Scan(&agent); err != nil {
		t.Fatal(err)
	}

	// The approver reads its own report.
	if listedReported(f) != 1 || streamedReported(f) != 1 {
		t.Fatal("approving person did not receive its report")
	}
	if readReported(t, f, f.tenantID, tenant.Principal{ID: f.person, TenantID: f.tenantID, Kind: tenant.Person}) != 1 {
		t.Fatal("approving person could not read its report")
	}

	// A workspace viewer sees every project and holds events.read, but has no
	// account access and is not the audience.
	second := uuid(t, f.db)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH i AS (INSERT INTO identities(issuer,subject,email) VALUES('fixture','viewer','viewer@example.test') RETURNING id) INSERT INTO principals(tenant_id,id,kind,identity_id,name,roles) SELECT $1,$2,'person',i.id,'Viewer person',ARRAY['member'] FROM i`, f.tenantID, second)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.db, f.tenantID, second, "viewer")
	owner := f.cookie
	f.cookie = f.call("POST", "/api/auth/dev-login", map[string]string{"email": "viewer@example.test"}, false, "", 200).Result().Cookies()[0]
	if n := listedReported(f); n != 0 {
		t.Fatalf("another person read %d reports from the history", n)
	}
	if n := streamedReported(f); n != 0 {
		t.Fatalf("another person received %d reports on the stream", n)
	}
	if n := readReported(t, f, f.tenantID, tenant.Principal{ID: second, TenantID: f.tenantID, Kind: tenant.Person}); n != 0 {
		t.Fatalf("another person read %d reports directly", n)
	}
	f.cookie = owner

	// The computer's own agent key, even capped by the approving person.
	agentPrincipal := tenant.Principal{ID: agent, TenantID: f.tenantID, Kind: tenant.Agent, Scopes: agentpairing.RuntimePermissions, KeyCreatorID: f.person}
	if n := readReported(t, f, f.tenantID, agentPrincipal); n != 0 {
		t.Fatalf("the agent key read %d reports", n)
	}
	var page struct {
		Items []struct {
			Type string `json:"type"`
		} `json:"items"`
	}
	agentList := f.request("GET", "/api/events?limit=200", nil, false, p.runtime)
	recorder := httptest.NewRecorder()
	f.h.ServeHTTP(recorder, agentList)
	if recorder.Code == 200 {
		decodeResult(t, recorder, &page)
		for _, e := range page.Items {
			if e.Type == reportedEvent {
				t.Fatal("the agent key listed a report")
			}
		}
	}

	// Another tenant reads nothing of this one, not even with every project
	// visible.
	foreign, err := tenantbootstrap.Create(t.Context(), f.db.App, "foreign-report", "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	if n := readReported(t, f, foreign, tenant.Principal{ID: f.person, TenantID: foreign, Kind: tenant.Person}); n != 0 {
		t.Fatalf("another tenant read %d reports", n)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, foreign, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type=$1`, reportedEvent).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatalf("another tenant's service path read %d reports", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The audience is the canonical person: a classic alias the person signs in
// with still reads the report, and a pairing with no approver publishes none.
func TestPairingReportAudienceFollowsTheCanonicalPerson(t *testing.T) {
	f := newFixture(t)
	alias := uuid(t, f.db)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name,roles,linked_to) VALUES($1,$2,'person','Classic alias',ARRAY['member'],$3)`, f.tenantID, alias, f.person)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := f.propose("claude")
	f.approve(p, "connect_only")
	f.redeem(p)
	f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "progress": agentpairing.SetupProgress{State: "connected"}}, false, "", 200)
	if n := readReported(t, f, f.tenantID, tenant.Principal{ID: alias, TenantID: f.tenantID, Kind: tenant.Person}); n != 1 {
		t.Fatalf("the person's alias read %d reports", n)
	}

	// Approver gone: nothing to address, nothing published.
	q := f.propose("claude")
	f.approve(q, "connect_only")
	f.redeem(q)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_requests SET approved_by=NULL WHERE id=$1`, q.id); err != nil {
		t.Fatal(err)
	}
	before := f.events(reportedEvent)
	f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": q.id, "lifecycle_secret": q.lifecycle, "progress": agentpairing.SetupProgress{State: "connected"}}, false, "", 200)
	if f.events(reportedEvent) != before {
		t.Fatal("a pairing without an approver published a report")
	}
}
