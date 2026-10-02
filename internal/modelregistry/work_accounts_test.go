// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type euClaudeEvidence struct{ expires time.Time }

func (f euClaudeEvidence) ResidencyClass(_ context.Context, _ pgx.Tx, a agentaccounts.Account, _ string) (string, string, *time.Time, error) {
	class := "any"
	if a.Harness == "claude" {
		class = "eu"
	}
	return class, "fixture-proof", &f.expires, nil
}
func TestWorkLatestPinAndAutomaticResidencyReselection(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		var runner string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Preference runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule()
		schedule.Override = "sprint"
		schedule.Reserve = capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		for _, h := range []string{"codex", "claude"} {
			var account string
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner)
    VALUES($1,$2,$2,'prefs-runner',$3,$2,now(),true,'prefs-generation',$4) RETURNING id::text`, p.TenantID, h, runner, p.ID).Scan(&account); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model)
    VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','requests',1000,'unrestricted')`, p.TenantID, account); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule)
    VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, account, raw); err != nil {
				return err
			}
		}
		a, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: "prefs-sol-61", Version: "1", Harness: "codex", Family: "openai", Model: "gpt-6.1-sol", Effort: "xhigh", Tier: "strong"})
		if err != nil {
			return err
		}
		b, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: "prefs-sol-62", Version: "1", Harness: "codex", Family: "openai", Model: "gpt-6.2-sol", Effort: "xhigh", Tier: "strong"})
		if err != nil {
			return err
		}
		scope, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "default"})
		if err != nil {
			return err
		}
		kind, _, err := modelprefs.LookupKind(t.Context(), tx, "backend", "")
		if err != nil {
			return err
		}
		row := modelprefs.Row{Cells: map[string]modelprefs.Cell{"normal": {Mode: "latest", Family: "openai", Line: "sol", Effort: "xhigh"}}}
		if err := modelprefs.PutRow(t.Context(), tx, p, scope, kind.ID, row); err != nil {
			return err
		}
		q := WorkQuery{Role: "build", Area: "backend"}
		now := time.Now()
		got, err := ResolveWork(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if got.Profile == nil || got.Profile.ID != b.ID || got.Role != "build" || got.Trace.LatestResolvedTo != b.ID || len(got.Trace.QualifyingAccountIDs) != 1 {
			t.Fatal("latest failed", got)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by) VALUES($1,$2,'old preference',$3)`, p.TenantID, b.ID, p.ID); err != nil {
			return err
		}
		got, err = ResolveWork(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if got.Profile == nil || got.Profile.ID != a.ID || got.Trace.Fallback != "" {
			t.Fatal("retired latest selected", got)
		}
		row.Cells["normal"] = modelprefs.Cell{Mode: "pinned", ProfileID: b.ID}
		if err := modelprefs.PutRow(t.Context(), tx, p, scope, kind.ID, row); err != nil {
			return err
		}
		got, err = ResolveWork(t.Context(), tx, p, q, now)
		if err != nil {
			return err
		}
		if got.Profile == nil || got.Profile.ID == b.ID || !strings.Contains(got.Trace.Fallback, "retired") {
			t.Fatal("retired pin did not fall back", got)
		}
		if _, err := tx.Exec(t.Context(), `DELETE FROM model_pref_rows WHERE scope_id=$1`, scope.ID); err != nil {
			return err
		}
		eu := "eu"
		scope.Residency = &eu
		if _, err := modelprefs.SaveScope(t.Context(), tx, p, scope); err != nil {
			return err
		}
		ctx := agentaccounts.WithResidencyClassifier(t.Context(), euClaudeEvidence{now.Add(time.Hour)})
		for _, role := range []string{"scout", "mechanical", "build", "build-hard"} {
			got, err := ResolveWork(ctx, tx, p, WorkQuery{Role: role, Area: "backend"}, now)
			if err != nil {
				return err
			}
			if got.Profile == nil || got.Profile.Harness != "claude" || got.Role != role || !strings.Contains(got.CommandTemplate, "claude") || got.OwnerRequired {
				t.Fatal("H4 did not reselect", role, got)
			}
			selected := 0
			for _, c := range got.Ladder {
				if c.Selected {
					selected++
					if len(c.SkipReasons) != 0 {
						t.Fatal("selected skipped candidate", c)
					}
				}
			}
			if selected != 1 {
				t.Fatal("selection trace", got)
			}
		}
		return nil
	})
}
