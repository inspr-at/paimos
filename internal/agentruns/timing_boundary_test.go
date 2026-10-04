// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestTerminalTelemetrySurvivesTimingHistoryLimit(t *testing.T) {
	f := setup(t)
	for _, count := range []int{9999, 10000, 10001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			v := f.claim(t, f.run(t, f.order(t, nil)))
			start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET started_at=$2 WHERE id=$1`, v.ID, start); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,status,at)
					SELECT $1,$2,n,'status','waiting',$4::timestamptz FROM generate_series(1,$3::int) n`, f.person.TenantID, v.ID, count, start)
				return err
			})
			body := map[string]any{"sequence": count + 1, "kind": "finished", "input_tokens_delta": 7,
				"git_commits": []map[string]string{{"sha": "0123456789abcdef", "subject": "History boundary"}}}
			path := "/api/runs/" + v.ID + "/telemetry"
			f.call(t, f.agent, "POST", path, body, 200, &v)
			if v.Status != "completed" || v.EndedAt == nil || v.OutcomeDetail == nil || *v.OutcomeDetail != "committed" || v.InputTokens != 7 {
				t.Fatalf("terminal evidence lost: %+v", v)
			}
			if count > 10000 {
				if v.ActiveMS != nil || v.WaitingMS != nil {
					t.Fatalf("overflow timing must remain unknown: active=%v waiting=%v", v.ActiveMS, v.WaitingMS)
				}
			} else if v.ActiveMS == nil || *v.ActiveMS != 0 || v.WaitingMS == nil || *v.WaitingMS != *v.DurationMS {
				t.Fatalf("known all-wait history must have zero active time: %+v", v)
			}
			f.call(t, f.agent, "POST", path, body, 200, &v)
			if v.InputTokens != 7 {
				t.Fatal("terminal replay double-counted usage")
			}
		})
	}
}

func TestTerminalWithoutStatusClosesWaitingSpan(t *testing.T) {
	f := setup(t)
	v := f.claim(t, f.run(t, f.order(t, nil)))
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET started_at=$2 WHERE id=$1`, v.ID, start); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,status,at) VALUES($1,$2,1,'status','waiting',$3::timestamptz+interval '2 seconds'),($1,$2,2,'heartbeat',NULL,$3::timestamptz+interval '4 seconds')`, f.person.TenantID, v.ID, start)
		return err
	})
	body := map[string]any{"sequence": 3, "kind": "finished"}
	path := "/api/runs/" + v.ID + "/telemetry"
	f.call(t, f.agent, "POST", path, body, 200, &v)
	if v.Status != "completed" || v.ActiveMS == nil || *v.ActiveMS != 2000 || v.WaitingMS == nil || v.DurationMS == nil || *v.ActiveMS+*v.WaitingMS != *v.DurationMS {
		t.Fatalf("unclosed terminal wait: %+v", v)
	}
	f.call(t, f.agent, "POST", path, body, 200, &v)
	if v.ActiveMS == nil || *v.ActiveMS != 2000 {
		t.Fatalf("replay changed timing: %+v", v)
	}
}
