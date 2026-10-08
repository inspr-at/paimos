// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: new line versions wait forever, or advisory and legacy selection
// disagree with a live account's model grants, project fence or admission.
func TestAccountSuccessorsShareBoardAndLegacyQualification(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		ctx := t.Context()
		insert := func(slug, harness, family, model, effort string) (Profile, error) {
			return insertProfile(ctx, tx, p.TenantID, profileWrite{Slug: slug, Version: "1", Harness: harness, Family: family, Model: model, Effort: effort, Tier: "strong"})
		}
		pin, err := insert("successor-pin", "codex", "openai", "gpt-6-sol", "high")
		if err != nil {
			return err
		}
		next, err := insert("successor-next", "codex", "openai", "gpt-6.10-sol", "xhigh")
		if err != nil {
			return err
		}
		for _, tc := range []struct {
			slug, harness, family, model, effort string
			allowed                              bool
		}{
			{"equal-other-effort", "codex", "openai", "gpt-6-sol", "low", false},
			{"older", "codex", "openai", "gpt-5.9-sol", "high", false},
			{"other-line", "codex", "openai", "gpt-6.10-astra", "high", false},
			{"other-harness", "pi", "openai", "openai/gpt-6.10-sol", "high", false},
			{"numeric-segments", "codex", "openai", "gpt-6.2-sol", "high", true},
		} {
			profile, err := insert(tc.slug, tc.harness, tc.family, tc.model, tc.effort)
			if err != nil {
				return err
			}
			allowed, err := agentaccounts.AccountAllowsProfile(ctx, tx, agentaccounts.Account{Harness: "codex", AllowedProfileIDs: []string{pin.ID}}, profile.ID)
			if err != nil {
				return err
			}
			if allowed != tc.allowed {
				t.Fatalf("%s allowance=%v", tc.slug, allowed)
			}
		}
		profiles, err := listProfiles(ctx, tx)
		if err != nil {
			return err
		}
		for _, profile := range profiles {
			_, line, version := ProfileLine(profile)
			var identity []string
			if err := tx.QueryRow(ctx, `SELECT aeon_model_line($1,$2)`, profile.Harness, profile.Model).Scan(&identity); err != nil {
				return err
			}
			if !slices.Equal(identity, []string{line, version}) {
				t.Fatalf("line policy differs for %s: %v versus %s/%s", profile.Model, identity, line, version)
			}
		}
		var runner, account string
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Successor runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,allowed_model_profile_ids)
 VALUES($1,'successor','codex','successor-runner',$2,'Successor',now(),true,'generation',$3,ARRAY[$4::uuid]) RETURNING id::text`, p.TenantID, runner, p.ID, pin.ID).Scan(&account); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model)
 VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','requests',1000,'unrestricted')`, p.TenantID, account); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule()
		schedule.Override, schedule.Reserve = "sprint", capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, account, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE model_role_routes SET priority=priority+100 WHERE role='build'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2)`, p.TenantID, next.ID); err != nil {
			return err
		}
		var board string
		if err := tx.QueryRow(ctx, `INSERT INTO model_pref_profiles(tenant_id,scope,template,thinking,usage,revision) VALUES($1,'workspace','balanced','deep','balanced',1) RETURNING id::text`, p.TenantID).Scan(&board); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank) VALUES($1,$2,'other','first',ARRAY['openai:sol'])`, p.TenantID, board); err != nil {
			return err
		}
		now := time.Now()
		for _, tc := range []struct {
			label    string
			grants   []string
			probe    bool
			selected bool
		}{
			{"pinned predecessor", []string{pin.ID}, true, true},
			{"NULL harness catalog", nil, true, true},
			{"empty denies all", []string{}, true, false},
			{"offline successor", []string{pin.ID}, false, false},
		} {
			if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET allowed_model_profile_ids=$2::uuid[],last_probe_ok=$3 WHERE id=$1`, account, tc.grants, tc.probe); err != nil {
				return err
			}
			legacy, err := resolveRole(ctx, tx, resolveQuery{Role: "build", Harness: "codex"}, now)
			if err != nil {
				return err
			}
			placement, err := resolveBoardWork(ctx, tx, p, WorkQuery{Role: "build", Area: "other", Situation: "first"}, now, nil)
			if err != nil {
				return err
			}
			if placement == nil || (legacy.Profile != nil) != tc.selected || (placement.Profile != nil) != tc.selected {
				t.Fatalf("%s board and ladder disagree: %+v / %+v", tc.label, placement, legacy)
			}
			if tc.selected && (legacy.Profile.ID != next.ID || placement.Profile.ID != next.ID || !slices.Equal(placement.Trace.QualifyingAccountIDs, []string{account})) {
				t.Fatalf("%s wrong successor/account", tc.label)
			}
		}
		// A newly registered future version needs no account metadata rewrite.
		future, err := insert("future-successor", "codex", "openai", "gpt-7-sol", "high")
		if err != nil {
			return err
		}
		allowed, err := agentaccounts.AccountAllowsProfile(ctx, tx, agentaccounts.Account{Harness: "codex", AllowedProfileIDs: []string{pin.ID}}, future.ID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("future registered successor needs a manual grant")
		}
		return nil
	})
}
