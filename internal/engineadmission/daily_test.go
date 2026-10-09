// SPDX-License-Identifier: AGPL-3.0-only
package engineadmission

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
)

// Risk: shadow admission reports allow with exhausted or unreadable daily
// data, or omits the person's local-midnight wait from its durable decision.
func TestAdmissionDailyLimitUnknownAndMidnightWait(t *testing.T) {
	f := newFixture(t)
	f.enable(t)
	f.decide(t, f.request("daily-under", "first_build"), "allowed")
	_, end, err := agentplan.LocalDay(f.at, "Europe/Vienna")
	must(t, err)
	f.exec(t, `INSERT INTO personal_profiles(tenant_id,principal_id,timezone) VALUES($1,$2,'Europe/Vienna')`, f.person.TenantID, f.person.ID)
	d := agentplan.DefaultDaily()
	d.AtLimit = "wait"
	d.BoostToday = &agentplan.DailyBoost{LimitUsedPct: 20, EnteredAs: "used", Until: end}
	raw, err := json.Marshal(agentplan.Plan{Total: 5, Daily: map[string]agentplan.DailySettings{"codex": d}})
	must(t, err)
	f.exec(t, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working',$3)`, f.person.TenantID, f.person.ID, raw)
	out := f.decide(t, f.request("daily-wait", "first_build"), "daily_limit")
	if out.RetryAfter == nil || *out.RetryAfter != int64(end.Sub(f.at)/time.Second) {
		t.Fatalf("wrong midnight wait: %+v", out)
	}
	d.AtLimit = "ladder"
	raw, err = json.Marshal(agentplan.Plan{Total: 5, Daily: map[string]agentplan.DailySettings{"codex": d}})
	must(t, err)
	f.exec(t, `UPDATE user_preferences SET value=$2 WHERE principal_id=$1 AND key='agents.working'`, f.person.ID, raw)
	f.decide(t, f.request("daily-ladder", "fix"), "daily_limit")
	f.exec(t, `UPDATE user_preferences SET value='{"total":5,"daily":{"codex":null}}' WHERE principal_id=$1 AND key='agents.working'`, f.person.ID)
	f.decide(t, f.request("daily-malformed", "review"), "daily_limit_unknown")
	f.exec(t, `UPDATE user_preferences SET value='{"total":5}' WHERE principal_id=$1 AND key='agents.working'`, f.person.ID)
	f.at = f.at.Add(2*time.Minute + time.Nanosecond)
	f.decide(t, f.request("daily-stale", "merge"), "daily_limit_unknown")
	if f.count(t, `SELECT count(*) FROM agent_runs`) > 0 || f.count(t, `SELECT count(*) FROM account_reservations`) > 0 {
		t.Fatal("advisory decision launched work")
	}
}
