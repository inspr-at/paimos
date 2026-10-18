// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"testing"
)

func TestHeldReplyRefusalMatchesUnknownParent(t *testing.T) {
	f := deliveryFixtureFor(t)
	parent := f.held(t)
	other := tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Person, Name: "other person"}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, other.TenantID, other.Name).Scan(&other.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, other.TenantID, other.ID, "owner")
	for _, tc := range []struct {
		name   string
		caller tenant.Principal
		patch  map[string]any
		role   string
	}{
		{"other person", other, nil, ""},
		{"recipient session mismatch", f.person, map[string]any{"recipient_session_id": f.otherSession}, ""},
		{"sender session mismatch", f.person, map[string]any{"sender_session_id": f.session}, ""},
		{"thread mismatch", f.person, map[string]any{"thread_id": "another-thread"}, ""},
		{"target mismatch", f.person, map[string]any{"to": f.otherAgent.ID}, ""},
		{"permission denied", f.person, nil, "viewer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.role != "" {
				dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, tc.role)
				defer dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "owner")
			}
			body := map[string]any{"to": f.agent.ID, "body": "answer", "idempotency_key": "refusal", "reply_to": parent}
			for k, v := range tc.patch {
				body[k] = v
			}
			path := "/api/projects/" + f.project + "/messages"
			held := request(t.Context(), f.mux, tc.caller, "POST", path, body)
			body["reply_to"] = uid()
			unknown := request(t.Context(), f.mux, tc.caller, "POST", path, body)
			if held.Code != 404 || unknown.Code != 404 || held.Body.String() != unknown.Body.String() || held.Header().Get("Content-Type") != unknown.Header().Get("Content-Type") {
				t.Errorf("held refusal differs from unknown: held=%d %s unknown=%d %s", held.Code, held.Body.String(), unknown.Code, unknown.Body.String())
			}
		})
	}
	if n := f.count(t, `SELECT count(*) FROM desk_questions`); n != 0 {
		t.Fatal("refused reply created a question")
	}
	if n := f.count(t, `SELECT count(*) FROM inbox_reply_obligations WHERE closed_at IS NOT NULL`); n != 0 {
		t.Fatal("refused reply settled a request")
	}
}
