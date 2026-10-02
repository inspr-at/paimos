// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type reviewLookupFailure struct {
	pgx.Tx
	profiles bool
	accounts int
	err      error
}

func (f *reviewLookupFailure) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	query := strings.Join(strings.Fields(sql), " ")
	if f.profiles && strings.Contains(query, "FROM model_profiles ORDER BY slug, version, id") {
		return nil, f.err
	}
	if !f.profiles && strings.HasPrefix(query, "SELECT id::text, account_key, harness, daemon_id") {
		f.accounts++
		if f.accounts == 2 {
			return nil, f.err
		}
	}
	return f.Tx.Query(ctx, sql, args...)
}

func TestResolveWorkPropagatesLateReviewLookupErrors(t *testing.T) {
	for _, profiles := range []bool{true, false} {
		name := "ReviewAccount"
		if profiles {
			name = "listProfiles"
		}
		t.Run(name, func(t *testing.T) {
			prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
				var runner, account string
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Qualified reviewer') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner)
 VALUES($1,'review-error','claude','review-error',$2,'Reviewer',now(),true,'generation',$3) RETURNING id::text`, p.TenantID, runner, p.ID).Scan(&account); err != nil {
					return err
				}
				schedule := capacity.DefaultSchedule()
				schedule.Override = "sprint"
				schedule.Reserve = capacity.ReserveOff
				raw, err := json.Marshal(schedule)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule)
 VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, account, raw); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model)
 VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','tokens',10000000,'unrestricted')`, p.TenantID, account); err != nil {
					return err
				}
				scope, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "default"})
				if err != nil {
					return err
				}
				var kindID string
				if err := tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='review'`).Scan(&kindID); err != nil {
					return err
				}
				if err := modelprefs.PutRow(t.Context(), tx, p, scope, kindID, modelprefs.Row{Cells: map[string]modelprefs.Cell{"normal": {Mode: "latest", Family: "anthropic", Line: "opus", Effort: "xhigh"}}}); err != nil {
					return err
				}
				q := WorkQuery{Role: "review-gate", AuthorFamily: "openai"}
				now := time.Now()
				injected := errors.New("review lookup unavailable")
				failing := &reviewLookupFailure{Tx: tx, profiles: profiles, err: injected}
				partial, err := ResolveReviewFor(t.Context(), failing, p, q, now)
				if partial.Profile == nil || !errors.Is(err, injected) {
					t.Fatalf("fault must follow a chosen profile: profile=%v error=%v", partial.Profile, err)
				}
				failing.accounts = 0
				out, err := ResolveWork(t.Context(), failing, p, q, now)
				if !errors.Is(err, injected) || out.CommandTemplate != "" {
					t.Fatalf("review error became a command: %+v, %v", out, err)
				}
				return nil
			})
		})
	}
}

func TestResolveAPIReturnsLoosenedResidencyTrace(t *testing.T) {
	var starter tenant.Principal
	var project string
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		starter = p
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'TRACE-P1','Trace project' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project); err != nil {
			return err
		}
		eu, any := "eu", "any"
		if _, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "person", PersonID: &p.ID, Residency: &eu, ResidencyLocked: true}); err != nil {
			return err
		}
		_, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "project", ProjectID: &project, Residency: &any})
		return err
	})
	got := decode[WorkResolution](t, &starter, http.MethodGet, "/api/models/resolve?role=build&project_id="+project, "", http.StatusOK)
	if got.Trace.PersonID == nil || *got.Trace.PersonID != starter.ID || got.Trace.Residency.Value != "any" || !got.Trace.Residency.LoosenedLock || len(got.Trace.Residency.LoosenedLocks) != 1 || got.Trace.Residency.LoosenedLocks[0] != (modelprefs.ResidencyLock{Level: "person", Value: "eu"}) {
		t.Fatalf("API dropped loosening trace: %+v", got.Trace)
	}
	status, _ := call(t, &starter, http.MethodGet, "/api/models/resolve?role=build&project_id=invalid", "")
	if status != http.StatusBadRequest {
		t.Fatal("invalid project must be a request error", status)
	}
}

func TestLatestPreferencesAcceptEveryProfileHarness(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		scope, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "default"})
		if err != nil {
			return err
		}
		kind, _, err := modelprefs.LookupKind(t.Context(), tx, "backend", "")
		if err != nil {
			return err
		}
		for _, harness := range []string{"codex", "claude", "pi", "cursor", "grok", "gemini", "opencode"} {
			if err := modelprefs.PutRow(t.Context(), tx, p, scope, kind.ID, modelprefs.Row{Cells: map[string]modelprefs.Cell{"normal": {Mode: "latest", Family: "google", Line: "gemini", Effort: "high", Harness: harness}}}); err != nil {
				return err
			}
			chain, err := modelprefs.LoadChain(t.Context(), tx, nil, "")
			if err != nil {
				return err
			}
			if got := modelprefs.ResolveCell(chain, kind.Slug, "normal"); got.Cell == nil || got.Cell.Harness != harness {
				t.Fatalf("latest harness lost in storage: %s, %+v", harness, got)
			}
		}
		expectConstraint(t, tx, "23514", `UPDATE model_pref_cells SET harness='terminal' WHERE scope_id=$1`, scope.ID)
		return nil
	})
}
