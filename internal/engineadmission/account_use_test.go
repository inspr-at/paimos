// SPDX-License-Identifier: AGPL-3.0-only
package engineadmission

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
)

// Risk C2: engine admission counts denied headroom/unknown readings or returns
// daily_limit_unknown instead of context when every owned door is denied.
func TestEngineAccountContextCapsContract(t *testing.T) {
	for _, tc := range []struct {
		name, reason                             string
		unknownDenied, allDenied, unknownAllowed bool
	}{
		{"a denied headroom", "daily_limit", false, false, false},
		{"b denied unknown", "daily_limit", true, false, false},
		{"c all denied", "context", false, true, false},
		{"d allowed unknown", "daily_limit_unknown", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.enable(t)
			var sibling string
			must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,owner_person_id,label,capacity_owner,linked_at,last_probe_at,last_probe_ok)
VALUES($1,'context-sibling','codex','sibling',$2,$3,'Denied sibling',$3,$4,$4,true) RETURNING id::text`, f.person.TenantID, f.agent.ID, f.person.ID, f.at).Scan(&sibling))
			if !tc.unknownDenied {
				f.exec(t, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly','context',10080,1,$3,$4,'harness')`, f.person.TenantID, sibling, f.at.Add(7*24*time.Hour), f.at)
			} else {
				f.exec(t, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly','context',10080,1,$3,$4,'harness')`, f.person.TenantID, sibling, f.at.Add(7*24*time.Hour), f.at.Add(-3*time.Minute))
			}
			// Preserve the allowed door; deny its sibling through actual matrix cells.
			f.exec(t, `INSERT INTO account_use_cells(tenant_id,account_id,context_id,source) SELECT $1,$2,id,'migration' FROM work_contexts WHERE tenant_id=$1 AND kind='default' ON CONFLICT DO NOTHING`, f.person.TenantID, f.account)
			f.exec(t, `DELETE FROM account_use_cells WHERE tenant_id=$1 AND account_id=$2`, f.person.TenantID, sibling)
			if tc.allDenied {
				f.exec(t, `DELETE FROM account_use_cells WHERE tenant_id=$1 AND account_id=$2`, f.person.TenantID, f.account)
			}
			_, end, err := agentplan.LocalDay(f.at, "UTC")
			must(t, err)
			d := agentplan.DefaultDaily()
			d.AtLimit = "ladder"
			d.BoostToday = &agentplan.DailyBoost{LimitUsedPct: 20, EnteredAs: "used", Until: end}
			raw, err := json.Marshal(agentplan.Plan{Total: 5, Daily: map[string]agentplan.DailySettings{"codex": d}})
			must(t, err)
			f.exec(t, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working',$3)`, f.person.TenantID, f.person.ID, raw)
			if tc.unknownAllowed {
				f.exec(t, `UPDATE account_capacity_readings SET read_at=$2 WHERE account_id=$1`, f.account, f.at.Add(-3*time.Minute))
			}
			out := f.decide(t, f.request("context-caps", "fix"), tc.reason)
			if tc.reason == "daily_limit" && (out.RetryAfter == nil || *out.RetryAfter != int64(end.Sub(f.at)/time.Second)) {
				t.Fatal("exhaustion lost until", out)
			}
			if tc.reason == "context" && out.RetryAfter != nil {
				t.Fatal("context/unknown invented until", out)
			}
			if f.count(t, `SELECT count(*) FROM engine_admission_decisions WHERE decision->>'reason'=$1`, tc.reason) != 1 || f.count(t, `SELECT count(*) FROM events WHERE type='engine.admission_decided' AND after->'decision'->>'reason'=$1`, tc.reason) != 1 {
				t.Fatal("decision/event lost reason")
			}
		})
	}
}
