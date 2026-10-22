// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"context"
	"errors"
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
func ForManagedSession(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, task, harness string, maximum int) (Merged, error) {
	return forManagedSession(ctx, tx, p, project, task, harness, maximum, false)
}

// ForManagedFiles retains every database layer and adds the authorized pin
// bytes separately, so agentd can put the repository delta before those layers.
func ForManagedFiles(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, task, harness string, maximum int) (Merged, error) {
	return forManagedSession(ctx, tx, p, project, task, harness, maximum, true)
}

func forManagedSession(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, task, harness string, maximum int, pinned bool) (Merged, error) {
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
	budget := &work{ctx: ctx}
	for _, s := range sets {
		if s.Scope.matches(c) && s.PublishedVersion != "" {
			if budget.rules >= maxBudgetRules {
				return Merged{}, errMergeStoreTooLarge
			}
			snap, e := loadVersion(ctx, tx, s.ID, s.PublishedVersion)
			if e != nil {
				return Merged{}, e
			}
			if e = budget.admit(snap); e != nil {
				return Merged{}, e
			}
			snapshots = append(snapshots, snap)
		}
	}
	limits, err := LoadBudget(ctx, tx)
	if err != nil {
		return Merged{}, err
	}
	var cat doctrine.Catalog
	if pinned {
		cat, err = doctrine.LoadPinnedCatalog(ctx, tx)
		if errors.Is(err, doctrine.ErrPinMissing) {
			return Merged{}, fail(409, "rules_pin_missing", "rules_pin_missing")
		}
		if errors.Is(err, doctrine.ErrPackTooLarge) {
			return Merged{}, fail(422, "rules_pack_too_large", "rules_pack_too_large")
		}
		if err != nil {
			return Merged{}, fail(503, "doctrine_unavailable", "Doctrine catalog unavailable; check source credential grants and server configuration.")
		}
	} else {
		cat, err = loadDoctrineCatalog(ctx, tx)
	}
	if err != nil {
		return Merged{}, err
	}
	var merged Merged
	if pinned {
		// Managed packs refuse overflow; they never use the legacy compatibility
		// cut, which would silently omit database rules during this transition.
		merged, err = MergeDeliveredWithin(c, snapshots, time.Now().UTC(), limits, cat)
		if err == nil {
			size := merged.ByteSize
			for _, file := range merged.PinnedFiles {
				size += len(file.Content) + len(file.Repository) + len(file.Path) + 128
			}
			if size > min(maximum, doctrine.ManagedPackMaxBytes) {
				return Merged{}, fail(422, "rules_pack_too_large", "rules_pack_too_large")
			}
		}
	} else {
		merged, err = MergeDeliveredForClient(c, snapshots, time.Now().UTC(), limits, maximum, cat)
	}
	if err != nil {
		return Merged{}, err
	}
	return merged, RecordServedManifest(ctx, tx, c, merged)
}
