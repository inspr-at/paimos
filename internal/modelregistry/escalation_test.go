// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestEscalationAreaPolicy(t *testing.T) {
	opus := Profile{Harness: "claude", Family: "anthropic", Model: "opus", Effort: "xhigh"}
	fable := opus
	fable.Model = "fable"
	astra := Profile{Harness: "codex", Family: "openai", Model: "gpt-6-astra", Effort: "xhigh"}
	sol := astra
	sol.Model = "gpt-6.1-sol"
	if escalationRank(opus, "ui") >= 99 || escalationRank(fable, "ui") < 99 || escalationRank(astra, "ui") < 99 || escalationRank(sol, "backend") < 99 {
		t.Fatal("work-area policy widened")
	}
	if escalationRank(fable, "design") < 99 || escalationRank(astra, "design") < 99 {
		t.Fatal("design work left the Opus route")
	}
	if escalationRank(fable, "backend") >= escalationRank(opus, "backend") || escalationRank(astra, "backend") >= 99 {
		t.Fatal("backend stronger routes missing")
	}
}
func TestEscalationUnknownAdmissionWaits(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		got, err := ResolveEscalation(t.Context(), tx, p, WorkQuery{Role: "build", Area: "backend"}, nil, time.Now())
		if err != nil {
			return err
		}
		if got.Profile != nil || got.Trace.Blocked != "admission_wait" {
			t.Fatal("missing accounts allowed dispatch or exhausted policy", got)
		}
		got, err = ResolveEscalation(t.Context(), tx, p, WorkQuery{Role: "review-gate", Area: "backend", AuthorFamily: "openai"}, nil, time.Now())
		if err != nil {
			return err
		}
		if got.Profile != nil || got.Trace.Blocked != "role has no automatic escalation" {
			t.Fatal("review gate switched", got)
		}
		return nil
	})
}

func TestEscalationRouteGrantsAndLocks(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		var runner, account, opus string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Escalation runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='claude-opus-xhigh'`).Scan(&opus); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,allowed_model_profile_ids) VALUES($1,'escalation-claude','claude','escalation-runner',$2,'Claude',now(),true,'generation',$3,ARRAY[$4::uuid]) RETURNING id::text`, p.TenantID, runner, p.ID, opus).Scan(&account); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','requests',1000,'unrestricted')`, p.TenantID, account); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule()
		schedule.Override = "sprint"
		schedule.Reserve = capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, account, raw); err != nil {
			return err
		}
		now := time.Now()
		q := WorkQuery{Role: "build", Area: "frontend"}
		unknown, err := ResolveEscalation(t.Context(), tx, p, q, nil, now)
		if err != nil {
			return err
		}
		if unknown.Profile != nil || unknown.Trace.Blocked != "admission_wait" {
			t.Fatal("manual cap guessed vendor room", unknown)
		}
		if _, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET capacity_kind='5h',capacity_source='agentd',capacity_read_at=$2 WHERE account_id=$1`, account, now); err != nil {
			return err
		}
		got, err := ResolveEscalation(t.Context(), tx, p, q, nil, now)
		if err != nil {
			return err
		}
		if got.Profile == nil || got.Profile.ID != opus || len(got.Trace.QualifyingAccountIDs) != 1 {
			t.Fatal("qualified UI route missing", got)
		}
		var project, ticket string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'ESC-1','Escalation project' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields) SELECT $1,id,'ESC-2','UI work',$2,'{"route_role":"build","area":"frontend"}' FROM node_kinds WHERE slug='work' RETURNING id::text`, p.TenantID, project).Scan(&ticket); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_escalations(tenant_id,ticket_node_id,project_id,state) VALUES($1,$2,$3,'{"status":"stuck"}')`, p.TenantID, ticket, project); err != nil {
			return err
		}
		ticketRoute, err := ResolveWork(t.Context(), tx, p, WorkQuery{TicketID: ticket}, now)
		if err != nil {
			return err
		}
		if ticketRoute.Profile == nil || ticketRoute.Profile.ID != opus {
			t.Fatal("ordinary ticket resolution did not consume escalation", ticketRoute)
		}
		if _, err := tx.Exec(t.Context(), `UPDATE work_escalations SET state='{"status":"awaiting_decision"}' WHERE ticket_node_id=$1`, ticket); err != nil {
			return err
		}
		ticketRoute, err = ResolveWork(t.Context(), tx, p, WorkQuery{TicketID: ticket}, now)
		if err != nil {
			return err
		}
		if ticketRoute.Profile != nil || !ticketRoute.OwnerRequired {
			t.Fatal("Decision Desk gate ignored", ticketRoute)
		}
		got, err = ResolveEscalation(t.Context(), tx, p, q, []string{opus}, now)
		if err != nil {
			return err
		}
		if got.Profile != nil || got.Trace.Blocked != "no allowed stronger route" {
			t.Fatal("repeated UI model allowed", got)
		}
		scope, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "default"})
		if err != nil {
			return err
		}
		kind, _, err := modelprefs.LookupKind(t.Context(), tx, "frontend", "")
		if err != nil {
			return err
		}
		if err := modelprefs.PutRow(t.Context(), tx, p, scope, kind.ID, modelprefs.Row{Locked: true, Cells: map[string]modelprefs.Cell{"normal": {Mode: "pinned", ProfileID: opus}}}); err != nil {
			return err
		}
		got, err = ResolveEscalation(t.Context(), tx, p, q, nil, now)
		if err != nil {
			return err
		}
		if got.Profile == nil || got.Profile.ID != opus {
			t.Fatal("allowed locked pin was ignored", got)
		}
		got, err = ResolveEscalation(t.Context(), tx, p, q, []string{opus}, now)
		if err != nil {
			return err
		}
		if got.Profile != nil || got.Trace.Blocked != "locked model preference" {
			t.Fatal("person lock bypassed", got)
		}
		return nil
	})
}
