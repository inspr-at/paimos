// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"net/http"
	"strings"
	"testing"
	"time"
)

// reviewLookupFailure fails the nth query matching match (n=0 only counts).
type reviewLookupFailure struct {
	pgx.Tx
	match func(string) bool
	seen  int
	fail  int
	err   error
}

func (f *reviewLookupFailure) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if f.match(strings.Join(strings.Fields(sql), " ")) {
		f.seen++
		if f.seen == f.fail {
			return nil, f.err
		}
	}
	return f.Tx.Query(ctx, sql, args...)
}

// Risk: a lookup failing after the reviewer was chosen becomes a review
// command or a silently different reviewer. Each fault is injected at its
// query's last use, which a counting dry run locates after the choice.
func TestResolveWorkPropagatesLateReviewLookupErrors(t *testing.T) {
	for name, match := range map[string]func(string) bool{
		"ReviewAccount": func(q string) bool { return strings.HasPrefix(q, "SELECT id::text, account_key, harness, daemon_id") },
		"accountUse":    func(q string) bool { return strings.Contains(q, "aeon_account_use_allowed_for_project") },
	} {
		t.Run(name, func(t *testing.T) {
			prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
				var runner, account string
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Qualified reviewer') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
					return err
				}
				dbtest.BindRoleWith(t, tx, p.TenantID, runner, "admin")
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
				var opus string
				if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='claude-opus-xhigh'`).Scan(&opus); err != nil {
					return err
				}
				// The Default's saved Review order (a review matrix pick before Models v2).
				if err := dbtest.ModelsOrder(t.Context(), tx, p.TenantID, "other", "review", "wait", opus); err != nil {
					return err
				}
				q := WorkQuery{Role: "review-gate", AuthorFamily: "openai", Harness: "claude"}
				now := time.Now()
				injected := errors.New("review lookup unavailable")
				failing := &reviewLookupFailure{Tx: tx, match: match, err: injected}
				if dry, err := ResolveReviewFor(t.Context(), failing, p, q, now); err != nil || dry.Profile == nil || dry.Account == nil || failing.seen == 0 {
					t.Fatalf("fixture must choose a reviewer through this lookup: %+v seen=%d %v", dry, failing.seen, err)
				}
				failing.fail, failing.seen = failing.seen, 0
				partial, err := ResolveReviewFor(t.Context(), failing, p, q, now)
				if partial.Profile == nil || !errors.Is(err, injected) {
					t.Fatalf("fault must follow a chosen profile: profile=%v error=%v", partial.Profile, err)
				}
				failing.fail, failing.seen = 0, 0
				if _, err := ResolveWork(t.Context(), failing, p, q, now); err != nil || failing.seen == 0 {
					t.Fatalf("dispatch must use this lookup: seen=%d %v", failing.seen, err)
				}
				failing.fail, failing.seen = failing.seen, 0
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
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'TRACE-1','Trace project' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project); err != nil {
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
