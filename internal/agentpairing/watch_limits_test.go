// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
)

func TestAttachComputerAdmissionUsesSharedLimit(t *testing.T) {
	f, key, in := leaseFixture(t)
	for range attachwatch.ComputerMax {
		in.RequestID = uuid(t, f.db)
		requestWatch(t, f, key, in)
	}
	in.RequestID = uuid(t, f.db)
	w := f.call("POST", "/api/agent-pairing/attach", in, false, key, 429)
	var body struct{ Error string }
	decodeResult(t, w, &body)
	if body.Error != "computer attach limit reached" {
		t.Fatal("admission refused for a different limit")
	}
}

func TestAttachAttemptAdmissionUsesSharedWindow(t *testing.T) {
	f := newFixture(t)
	// Seed a full lookup bucket well inside the shared window. No sleeps or
	// boundary timing: the request must refuse, then the injected old start
	// must let the next request reach code lookup.
	_, err := f.db.Admin.Exec(t.Context(), `INSERT INTO harness_attach_limits(tenant_id,bucket,attempts,starts_at) VALUES($1,'lookup',10,clock_timestamp())`, f.tenantID)
	if err != nil {
		t.Fatal(err)
	}
	lookup := map[string]string{"user_code": "000000000"}
	f.call("POST", "/api/agent-pairing/attach/lookup", lookup, true, "", 429)
	_, err = f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_limits SET starts_at=clock_timestamp()-($2::integer * interval '1 second') WHERE tenant_id=$1 AND bucket='lookup'`, f.tenantID, int((attachwatch.AttemptWindow+time.Minute)/time.Second))
	if err != nil {
		t.Fatal(err)
	}
	f.call("POST", "/api/agent-pairing/attach/lookup", lookup, true, "", 404)
	var attempts int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT attempts FROM harness_attach_limits WHERE tenant_id=$1 AND bucket='lookup'`, f.tenantID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("expired attempt window did not reset")
	}
}
