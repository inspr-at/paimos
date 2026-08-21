// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

// Canaries a hostile (or merely careless) caller can put on the wire.
// Every one is distinctive, so a substring hit anywhere in the database
// is proof of a leak rather than a coincidence.
const (
	canaryDeliveryKey = "CANARY-DELIVERY-809-ZZZ"
	canaryRunID       = "CANARY-RUN-809-ZZZ"
	canarySessionID   = "CANARY-SESSION-809-ZZZ"
	canaryIdempotency = "CANARY-IDEMPOTENCY-809-ZZZ"
	canaryOrigin      = "https://attacker.example/CANARY-ORIGIN-809-ZZZ"
	canaryReferer     = "https://attacker.example/CANARY-REFERER-809-ZZZ"
	canaryQuery       = "CANARY-QUERY-809-ZZZ"
	canaryBody        = "CANARY-BODY-809-ZZZ"
	canaryBearer      = "CANARY-BEARER-809-ZZZ"
	canaryDeviceID    = "CANARY-DEVICE-809-ZZZ"
	canaryRequestID   = "CANARY-REQUEST-ID-809-ZZZ"
	canaryForwardedIP = "CANARY-FORWARDED-IP-809-ZZZ"
	canaryRealIP      = "CANARY-REAL-IP-809-ZZZ"
)

func allControlCanaries() []string {
	return []string{
		canaryDeliveryKey, canaryRunID, canarySessionID, canaryIdempotency,
		canaryOrigin, canaryReferer, canaryQuery, canaryBody, canaryBearer, canaryDeviceID,
		canaryRequestID, canaryForwardedIP, canaryRealIP,
	}
}

// controlProbePaths is every frozen family, with the caller-supplied
// segment set to a canary.
func controlProbePaths() []string {
	return []string{
		"/api/agent-mode/deliveries/" + canaryDeliveryKey + "/control-capability-grants",
		"/api/agent-mode/deliveries/" + canaryDeliveryKey + "/control-commands",
		"/api/agent-mode/control-capability-grants/" + canaryDeliveryKey,
		"/api/agent-mode/control-commands/" + canaryDeliveryKey,
		"/api/runs/" + canaryRunID + "/control-capability-leases",
		"/api/runs/" + canaryRunID + "/input-requests",
		"/api/runs/" + canaryRunID + "/control-commands",
		"/api/control-capability-leases/" + canaryRunID,
		"/api/control-commands/" + canaryRunID,
	}
}

// postHostileControl sends a mutation with every caller-controlled field
// loaded with a canary.
func (ts *testServer) postHostileControl(t *testing.T, path, cookie string) *http.Response {
	t.Helper()
	body := bytes.NewReader([]byte(`{"note":"` + canaryBody + `"}`))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		ts.srv.URL+path+"?delivery="+canaryQuery, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", canaryOrigin)
	req.Header.Set("Referer", canaryReferer)
	req.Header.Set("X-PAIMOS-Session-Id", canarySessionID)
	req.Header.Set("Idempotency-Key", canaryIdempotency)
	req.Header.Set("X-PAIMOS-Device-Id", canaryDeviceID)
	req.Header.Set("Authorization", "Bearer "+canaryBearer)
	req.Header.Set("X-PAIMOS-Request-Id", canaryRequestID)
	req.Header.Set("X-Forwarded-For", canaryForwardedIP)
	req.Header.Set("X-Real-IP", canaryRealIP)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func countSessionActivity(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM session_activity`).Scan(&n); err != nil {
		t.Fatalf("count session_activity: %v", err)
	}
	return n
}

// PAI-809: control mutations write no session_activity row at all — not a
// redacted one, not a NULL-path one. The mandatory control_events record
// is the authoritative account; this convenience table must hold nothing.
func TestControlMutationsSkipSessionActivityEntirely(t *testing.T) {
	t.Setenv("PAIMOS_AUDIT_SESSIONS", "")
	ts := newTestServer(t)

	// The audit table is live: an ordinary mutation still lands.
	resp := ts.post(t, "/api/tags", ts.adminCookie, map[string]string{"name": "control-audit-baseline"})
	resp.Body.Close()
	if countSessionActivity(t) == 0 {
		t.Fatal("session audit is not recording — the rest of this test would pass vacuously")
	}

	for _, path := range controlProbePaths() {
		t.Run(path, func(t *testing.T) {
			before := countSessionActivity(t)
			resp := ts.postHostileControl(t, path, ts.adminCookie)
			resp.Body.Close()
			if after := countSessionActivity(t); after != before {
				t.Fatalf("control mutation wrote %d session_activity row(s)", after-before)
			}
		})
	}

	// A near-miss keeps ordinary behavior: it is still audited.
	before := countSessionActivity(t)
	resp = ts.postHostileControl(t, "/api/control-commands/"+canaryRunID+"/extra", ts.adminCookie)
	resp.Body.Close()
	if countSessionActivity(t) <= before {
		t.Fatal("near-miss path stopped being audited — redaction bled outside the frozen families")
	}
}

// The broad canary: after a control mutation, no table anywhere in the
// schema holds any caller-supplied value. This covers session_activity by
// construction and catches any future middleware that starts persisting
// request material without asking.
func TestControlMutationsLeaveNoCanaryInAnyTable(t *testing.T) {
	t.Setenv("PAIMOS_AUDIT_SESSIONS", "")
	ts := newTestServer(t)

	for _, path := range controlProbePaths() {
		resp := ts.postHostileControl(t, path, ts.adminCookie)
		resp.Body.Close()
	}

	tables := listSQLiteTables(t)
	if len(tables) < 10 {
		t.Fatalf("only %d tables found — the sweep is not covering the schema", len(tables))
	}
	for _, table := range tables {
		for column, value := range scanTableText(t, table) {
			for _, canary := range allControlCanaries() {
				if strings.Contains(value, canary) {
					t.Fatalf("%s.%s persisted control canary %q: %q", table, column, canary, value)
				}
			}
		}
	}
}

func TestClassifiedControlRefusalsArePrivateAndReflectNoCanary(t *testing.T) {
	ts := newTestServer(t)
	for _, path := range controlProbePaths() {
		t.Run(path, func(t *testing.T) {
			response := ts.postHostileControl(t, path, ts.adminCookie)
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if got := response.Header.Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
			wire := string(body)
			for name, values := range response.Header {
				wire += "\n" + name + ":" + strings.Join(values, ",")
			}
			for _, canary := range allControlCanaries() {
				if strings.Contains(wire, canary) {
					t.Fatalf("control refusal reflected %q: %s", canary, wire)
				}
			}
			if strings.Contains(string(body), `"instance"`) {
				t.Fatalf("control refusal exposed an instance: %s", body)
			}
		})
	}
}

func listSQLiteTables(t *testing.T) []string {
	t.Helper()
	rows, err := db.DB.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out = append(out, name)
	}
	return out
}

// scanTableText returns every cell of a table as text, keyed by
// "column#row" so a failure names where it found the canary.
func scanTableText(t *testing.T, table string) map[string]string {
	t.Helper()
	// #nosec G202 -- table names come from sqlite_master in this test DB.
	rows, err := db.DB.Query(`SELECT * FROM "` + table + `"`)
	if err != nil {
		t.Fatalf("select %s: %v", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns %s: %v", table, err)
	}
	out := map[string]string{}
	for index := 0; rows.Next(); index++ {
		cells := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range cells {
			pointers[i] = &cells[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		for i, cell := range cells {
			out[fmt.Sprintf("%s#%d", columns[i], index)] = cellText(cell)
		}
	}
	return out
}

func cellText(cell any) string {
	switch typed := cell.(type) {
	case nil:
		return ""
	case []byte:
		return string(typed)
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", typed)
	}
}
