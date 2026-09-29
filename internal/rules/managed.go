// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// ForManagedSession is an execution-scoped read, not general rules.read. The
// harness handler has authenticated the exact managed run and worker lease.
// Selectors come from that row, never from the agent's request. Paired keys keep
// their existing ceiling and gain no route to other people's/project rules.
func ForManagedSession(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, task, harness string) (Merged, error) {
	if p.Kind != tenant.Agent {
		return Merged{}, authz.ErrForbidden
	}
	owner, err := actorOwner(ctx, tx, p)
	if err != nil {
		return Merged{}, err
	}
	c := Context{TenantID: p.TenantID, ProjectID: project, PersonID: owner, AgentID: p.ID, Role: "builder", Harness: harness, TaskID: task}
	if err = ValidateContext(c); err != nil {
		return Merged{}, err
	}
	// Same read-only rules RLS envelope as the rules endpoint, narrowed to
	// exactly this project. No generic API operation follows this scoped read.
	if _, err = tx.Exec(ctx, `SELECT set_config('aeon.rules_projects',$3,true),set_config('aeon.rules_owner',$1,true),set_config('aeon.rules_agent',$2,true),set_config('aeon.rules_access','on',true),set_config('aeon.visible_projects','*',true)`, owner, p.ID, "{"+project+"}"); err != nil {
		return Merged{}, err
	}
	sets, err := allSets(ctx, tx, "")
	if err != nil {
		return Merged{}, err
	}
	snapshots := []Snapshot{}
	for _, s := range sets {
		if s.Scope.matches(c) && s.PublishedVersion != "" {
			snap, e := loadVersion(ctx, tx, s.ID, s.PublishedVersion)
			if e != nil {
				return Merged{}, e
			}
			snapshots = append(snapshots, snap)
		}
	}
	cat, err := doctrine.LoadCatalog(ctx, tx)
	if err != nil {
		return Merged{}, err
	}
	return MergeDelivered(c, snapshots, time.Now().UTC(), cat)
}
