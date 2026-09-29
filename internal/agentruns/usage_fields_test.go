// SPDX-License-Identifier: AGPL-3.0-only

package agentruns_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentruns"
)

func TestRunUsageFields(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	first := f.run(t, o)
	var retry agentruns.Run
	f.call(t, f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{
		"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "retry_of_run_id": first.ID,
	}, 201, &retry)
	if retry.RetryOfRunID == nil || *retry.RetryOfRunID != first.ID {
		t.Fatalf("retry: %+v", retry)
	}
	other := f.order(t, nil)
	f.call(t, f.person, "POST", "/api/work-orders/"+other.NodeID+"/runs", map[string]any{
		"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "retry_of_run_id": first.ID,
	}, 400, nil)
	f.call(t, f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{
		"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "retry_of_run_id": "not-a-uuid",
	}, 400, nil)
	f.call(t, f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{
		"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "retry_of_run_id": uuid(),
	}, 400, nil)

	v := f.claim(t, f.run(t, o))
	path := "/api/runs/" + v.ID + "/telemetry"
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 1, "kind": "finished", "status": "completed", "git_commits": []map[string]string{{"sha": "nope", "subject": "Add usage"}}}, 400, nil)
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 1, "kind": "status", "status": "running"}, 200, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET started_at=clock_timestamp()-interval '10 seconds' WHERE id=$1`, v.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE run_telemetry SET at=clock_timestamp()-interval '10 seconds' WHERE run_id=$1 AND sequence=1`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 2, "kind": "status", "status": "waiting"}, 200, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE run_telemetry SET at=clock_timestamp()-interval '4 seconds' WHERE run_id=$1 AND sequence=2`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, map[string]any{
		"sequence": 3, "kind": "finished", "status": "completed",
		"git_commits": []map[string]string{{"sha": "0123456789abcdef", "subject": "Add usage"}},
	}, 200, &v)
	if v.OutcomeDetail == nil || *v.OutcomeDetail != "committed" || v.ActiveMS == nil || v.DurationMS == nil {
		t.Fatalf("committed run: %+v", v)
	}
	var marks [3]time.Time
	f.tx(t, f.person, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT at FROM run_telemetry WHERE run_id=$1 ORDER BY sequence`, v.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for i := 0; rows.Next(); i++ {
			if i >= len(marks) {
				t.Fatal("extra telemetry")
			}
			if err = rows.Scan(&marks[i]); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	waiting := marks[2].Sub(marks[1]).Milliseconds()
	if waiting <= 0 {
		t.Fatalf("waiting gap %d marks %v", waiting, marks)
	}
	want := *v.DurationMS - waiting
	if want < 0 {
		want = 0
	}
	if *v.ActiveMS != want || *v.ActiveMS >= *v.DurationMS {
		t.Fatalf("active %d duration %d waiting %d", *v.ActiveMS, *v.DurationMS, waiting)
	}

	plain := f.claim(t, f.run(t, o))
	f.call(t, f.agent, "POST", "/api/runs/"+plain.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "completed"}, 200, &plain)
	if plain.OutcomeDetail == nil || *plain.OutcomeDetail != "no_commit" || plain.ActiveMS == nil || plain.DurationMS == nil || *plain.ActiveMS != *plain.DurationMS {
		t.Fatalf("no commit: %+v", plain)
	}
	abandoned := f.claim(t, f.run(t, o))
	f.call(t, f.agent, "POST", "/api/runs/"+abandoned.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "cancelled"}, 200, &abandoned)
	if abandoned.OutcomeDetail == nil || *abandoned.OutcomeDetail != "abandoned" {
		t.Fatalf("abandoned: %+v", abandoned)
	}

	opened := f.claim(t, f.run(t, o))
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO work_evidence(tenant_id,work_order_id,submitted_by_principal_id,kind,reference,run_id) VALUES($1,$2,$3,'url',$4,$5)`, f.person.TenantID, o.NodeID, f.agent.ID, "https://example.com/acme/repo/pull/4", opened.ID)
		return err
	})
	f.call(t, f.agent, "POST", "/api/runs/"+opened.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "completed"}, 200, &opened)
	if opened.OutcomeDetail == nil || *opened.OutcomeDetail != "pr_opened" {
		t.Fatalf("pr opened: %+v", opened)
	}
	merged := f.claim(t, f.run(t, o))
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO work_evidence(tenant_id,work_order_id,submitted_by_principal_id,kind,reference,run_id) VALUES($1,$2,$3,'url',$4,$5)`, f.person.TenantID, o.NodeID, f.agent.ID, "https://example.com/acme/repo/pull/4/merge", merged.ID)
		return err
	})
	f.call(t, f.agent, "POST", "/api/runs/"+merged.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "completed"}, 200, &merged)
	if merged.OutcomeDetail == nil || *merged.OutcomeDetail != "pr_opened" {
		t.Fatalf("pull merge URL: %+v", merged)
	}
	word := f.claim(t, f.run(t, o))
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO work_evidence(tenant_id,work_order_id,submitted_by_principal_id,kind,reference,run_id) VALUES($1,$2,$3,'url',$4,$5)`, f.person.TenantID, o.NodeID, f.agent.ID, "https://github.com/acme/merged-tools/pull/4", word.ID)
		return err
	})
	f.call(t, f.agent, "POST", "/api/runs/"+word.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "completed"}, 200, &word)
	if word.OutcomeDetail == nil || *word.OutcomeDetail != "pr_opened" {
		t.Fatalf("merged word in URL: %+v", word)
	}
	real := f.claim(t, f.run(t, o))
	f.call(t, f.agent, "POST", "/api/runs/"+real.ID+"/telemetry", map[string]any{
		"sequence": 1, "kind": "finished", "status": "completed",
		"git_commits": []map[string]any{{"sha": "0123456789abcdef", "subject": "Merge feature", "parents": 2, "on_default_branch": true}},
	}, 200, &real)
	if real.OutcomeDetail == nil || *real.OutcomeDetail != "merged" {
		t.Fatalf("merge commit: %+v", real)
	}
	subjectOnly := f.claim(t, f.run(t, o))
	f.call(t, f.agent, "POST", "/api/runs/"+subjectOnly.ID+"/telemetry", map[string]any{
		"sequence": 1, "kind": "finished", "status": "completed",
		"git_commits": []map[string]any{{"sha": "0123456789abcdef", "subject": "Merge branch 'main'", "parents": 1, "on_default_branch": true}},
	}, 200, &subjectOnly)
	if subjectOnly.OutcomeDetail == nil || *subjectOnly.OutcomeDetail != "committed" {
		t.Fatalf("merge subject: %+v", subjectOnly)
	}

	sessionRun := f.claim(t, f.run(t, o))
	f.tx(t, f.person, func(tx pgx.Tx) error {
		project, session := uuid(), uuid()
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title) SELECT $1,$2,id,'AEON300-1','Usage' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,run_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,commits) VALUES($1,$2,$3,$4,$5,'codex','test','unmanaged','worker','unknown','{}',decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex'),$6::jsonb)`, f.person.TenantID, session, project, f.agent.ID, sessionRun.ID, `[{"sha":"0123456789abcdef","subject":"Add usage"}]`)
		return err
	})
	f.call(t, f.agent, "POST", "/api/runs/"+sessionRun.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "completed"}, 200, &sessionRun)
	if sessionRun.OutcomeDetail == nil || *sessionRun.OutcomeDetail != "committed" {
		t.Fatalf("session commits: %+v", sessionRun)
	}
}

func TestHeartbeatKeepsWaiting(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	v := f.claim(t, f.run(t, o))
	path := "/api/runs/" + v.ID + "/telemetry"
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 1, "kind": "status", "status": "running"}, 200, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET started_at=clock_timestamp()-interval '10 seconds' WHERE id=$1`, v.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE run_telemetry SET at=clock_timestamp()-interval '10 seconds' WHERE run_id=$1 AND sequence=1`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 2, "kind": "status", "status": "waiting"}, 200, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE run_telemetry SET at=clock_timestamp()-interval '8 seconds' WHERE run_id=$1 AND sequence=2`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 3, "kind": "heartbeat"}, 200, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE run_telemetry SET at=clock_timestamp()-interval '4 seconds' WHERE run_id=$1 AND sequence=3`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 4, "kind": "finished", "status": "completed"}, 200, &v)
	if v.ActiveMS == nil || v.DurationMS == nil {
		t.Fatalf("usage: %+v", v)
	}
	var marks [4]time.Time
	var heartbeatStatus *string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT at, status FROM run_telemetry WHERE run_id=$1 ORDER BY sequence`, v.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for i := 0; rows.Next(); i++ {
			if i >= len(marks) {
				t.Fatal("extra telemetry")
			}
			var status *string
			if err = rows.Scan(&marks[i], &status); err != nil {
				return err
			}
			if i == 2 {
				heartbeatStatus = status
			}
		}
		return rows.Err()
	})
	if heartbeatStatus != nil {
		t.Fatalf("heartbeat stored status %q", *heartbeatStatus)
	}
	waiting := marks[3].Sub(marks[1]).Milliseconds()
	if waiting <= 0 {
		t.Fatalf("waiting gap %d marks %v", waiting, marks)
	}
	want := *v.DurationMS - waiting
	if want < 0 {
		want = 0
	}
	if *v.ActiveMS != want {
		t.Fatalf("active %d want %d duration %d waiting %d", *v.ActiveMS, want, *v.DurationMS, waiting)
	}
}
