// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestCutoverLinksKeepSourceIdentity(t *testing.T) {
	f := setup(t)
	q := f.ask(t, f.project, "carries_on", nil)
	a := f.approval(t, f.ticket, time.Now().Add(time.Hour))
	page, err := f.m.Read(t.Context(), f.person, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Counts.Open != 2 {
		t.Fatalf("source count: %+v", page)
	}
	want := map[string]string{q.ID: "q:", a: "a:"}
	for _, item := range page.Items {
		if item.Href != "/decision-desk?item="+want[item.ID]+item.ID || item.Source == "" {
			t.Fatalf("source identity/link lost: %+v", item)
		}
	}
}

func TestTierCutoverUsesNativeRowsAndCurrentProjectPermissions(t *testing.T) {
	f := setup(t)
	makeRequest := func(project string) (string, string) {
		var session, request string
		if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,capabilities,ref_digest,lease_digest)
		 VALUES($1,$2,$3,'codex','cutover-test','managed','worker',ARRAY['service_tier_v1'],gen_random_uuid()::text::bytea,gen_random_uuid()::text::bytea) RETURNING id::text`, f.person.TenantID, project, f.agent.ID).Scan(&session); err != nil {
			t.Fatal(err)
		}
		if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO harness_tier_requests(tenant_id,id,session_id,tier,reason,requested_by_principal_id)
		 VALUES($1,gen_random_uuid(),$2,'fast','Native protected request',$3) RETURNING id::text`, f.person.TenantID, session, f.agent.ID).Scan(&request); err != nil {
			t.Fatal(err)
		}
		return session, request
	}
	session, visible := makeRequest(f.project)
	_, hidden := makeRequest(f.otherProject)
	read := func(p tenant.Principal) Page {
		page, err := f.m.Read(t.Context(), p, 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	if page := read(f.person); page.Counts.Open != 2 {
		t.Fatalf("owner missing native requests: %+v", page)
	}
	page := read(f.reader)
	if page.Counts.Open != 1 || len(page.Items) != 1 || page.Items[0].ID != visible || page.Items[0].ID == hidden {
		t.Fatalf("mixed project access: %+v", page)
	}
	item := page.Items[0]
	if item.Kind != "tier_request" || item.Revision != 0 || item.Href != "/decision-desk?item=t:"+visible || !strings.Contains(item.Source, session) || item.PushEligible(time.Now()) {
		t.Fatalf("native protected pointer: %+v", item)
	}
	f.exec(t, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.reader.TenantID, f.reader.ID)
	if page := read(f.reader); page.Counts.Open != 0 {
		t.Fatalf("revoked access still counted: %+v", page)
	}
	f.exec(t, `UPDATE harness_tier_requests SET state='declined',decided_at=clock_timestamp(),decided_by_principal_id=$2 WHERE id=$1`, visible, f.person.ID)
	if page := read(f.person); page.Counts.Open != 1 || page.Items[0].ID != hidden {
		t.Fatalf("settled native row still counted: %+v", page)
	}
}
