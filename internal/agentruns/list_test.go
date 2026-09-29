// SPDX-License-Identifier: AGPL-3.0-only

package agentruns_test

import (
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestRunHistoryPagingFiltersAndTelemetry(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	a, b, c := f.run(t, o), f.run(t, o), f.run(t, o)
	session, project := uuid(), uuid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET created_at='2026-01-01T00:00:00Z'`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed',started_at='2026-01-01T00:00:00Z',ended_at='2026-01-01T00:00:02.123Z',effective_model='effective-test',model_evidence='vendor_reported',input_tokens=123,output_tokens=45,cost_micros=9007199254740993 WHERE id=$1`, a.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET agent_principal_id=$2 WHERE id=$1`, c.ID, f.other.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title) SELECT $1,$2,id,'RLP-1','History' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,run_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'codex','test','unmanaged','worker','unknown','{}',decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex'))`, f.person.TenantID, session, project, f.agent.ID, a.ID)
		return err
	})
	type page struct {
		Items []agentruns.Run `json:"items"`
		Next  *string         `json:"next_cursor"`
	}
	get := func(p tenant.Principal, path string) page {
		var out page
		f.call(t, p, "GET", path, nil, 200, &out)
		return out
	}
	seen := map[string]bool{}
	path := "/api/runs?limit=1"
	var cursor, last string
	for {
		out := get(f.person, path)
		if len(out.Items) != 1 {
			t.Fatal(out)
		}
		run := out.Items[0]
		if seen[run.ID] || last != "" && run.ID >= last {
			t.Fatal("unstable order")
		}
		last = run.ID
		seen[run.ID] = true
		if out.Next == nil {
			break
		}
		cursor = *out.Next
		path = "/api/runs?limit=1&cursor=" + url.QueryEscape(cursor)
	}
	if len(seen) != 3 || !seen[b.ID] {
		t.Fatal(seen)
	}
	for query, want := range map[string]int{"session=" + session: 1, "session=" + uuid(): 0, "agent=" + f.agent.ID: 2, "work_order=" + o.NodeID: 3, "work_order=" + uuid(): 0, "session=" + session + "&agent=" + f.other.ID: 0} {
		if got := len(get(f.person, "/api/runs?"+query).Items); got != want {
			t.Fatalf("%s: %d want %d", query, got, want)
		}
	}
	got := get(f.person, "/api/runs?session="+session).Items[0]
	if got.Outcome == nil || *got.Outcome != "completed" || got.DurationMS == nil || *got.DurationMS != 2123 || got.EffectiveModel == nil || *got.EffectiveModel != "effective-test" || got.InputTokens != 123 || got.OutputTokens != 45 || got.Cost != 9007199254740993 || got.ActiveMS != nil || got.OutcomeDetail != nil || got.RetryOfRunID != nil {
		t.Fatalf("telemetry %+v", got)
	}
	var queued agentruns.Run
	f.call(t, f.person, "GET", "/api/runs/"+b.ID, nil, 200, &queued)
	if queued.Outcome != nil || queued.DurationMS != nil {
		t.Fatal("fabricated terminal metrics")
	}
	if len(get(f.agent, "/api/runs").Items) != 2 {
		t.Fatal("agent visibility")
	}
	if len(get(f.foreign, "/api/runs?session="+session).Items) != 0 {
		t.Fatal("tenant leak")
	}
	f.call(t, f.agent, "GET", "/api/runs?agent="+f.other.ID, nil, 403, nil)
	for _, query := range []string{"limit=201", "limit=0", "cursor=bad", "agent=no", "session=no", "work_order=no", "agent=" + f.agent.ID + "&cursor=" + cursor} {
		f.call(t, f.person, "GET", "/api/runs?"+query, nil, 400, nil)
	}
	f.call(t, f.foreign, "GET", "/api/runs?cursor="+cursor, nil, 400, nil)
	f.call(t, tenant.Principal{}, "GET", "/api/runs", nil, 401, nil)
	f.token = f.key(t, f.agent, []string{"run.create"})
	f.call(t, f.agent, "GET", "/api/runs", nil, 403, nil)
}
