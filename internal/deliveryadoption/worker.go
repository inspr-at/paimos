// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/jackc/pgx/v5"
)

type PassResult struct {
	NextTenant          string `json:"next_tenant,omitempty"`
	Discovered          int    `json:"discovered"`
	Project             string `json:"project,omitempty"`
	State               string `json:"state,omitempty"`
	Reason              string `json:"reason,omitempty"`
	DiscoveryIncomplete bool   `json:"discovery_incomplete"`
}

// Run starts at instance feature activation. It uses no project allowlist or
// opt-in gate; deployment prerequisites are independently verified beforehand.
// One bounded pass at a time, with persisted per-project resume checkpoints.
func (s *Service) Run(ctx context.Context) {
	cursor := ""
	for {
		if ctx.Err() != nil {
			return
		}
		result, _ := s.Pass(ctx, cursor)
		cursor = result.NextTenant
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) Pass(ctx context.Context, cursor string) (PassResult, error) {
	out := PassResult{}
	if !s.cfg.RecoveryReconciled {
		// Restored checkpoints are not authority for claims or cleanup while
		// the restore history is being validated.
		out.Reason = "rollout_dependency"
		return out, ErrPrerequisite
	}
	next, n, discoveryErr := s.Discover(ctx, cursor)
	out.NextTenant = next
	out.Discovered = n
	out.DiscoveryIncomplete = next != "" || discoveryErr != nil
	// A missing tenant authority is an explicit instance failure. Advance past
	// that tenant so existing legitimate peers can still make progress.
	if discoveryErr != nil && next != "" {
		var after string
		if err := s.pool.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id>$1::uuid ORDER BY id LIMIT 1`, next).Scan(&after); err == nil {
			out.NextTenant = after
		} else if errors.Is(err, pgx.ErrNoRows) {
			out.NextTenant = ""
		}
	}
	if s.provider != nil {
		if err := s.Reconcile(ctx); err != nil {
			out.Reason = "cleanup_blocked"
		}
	}
	prerequisiteErr := s.prerequisites(ctx)
	selection, stop := context.WithTimeout(ctx, 60*time.Second)
	defer stop()
	for _, a := range s.cfg.Authorities {
		var project string
		err := s.with(selection, a.Executor, func(tx pgx.Tx) error {
			return tx.QueryRow(selection, `SELECT project_node_id::text FROM delivery_adoption_jobs WHERE instance_id=$1 AND state<>'adopted' AND next_attempt_at<=$2 AND (lease_until IS NULL OR lease_until<=$2) ORDER BY next_attempt_at,project_node_id LIMIT 1`, s.cfg.Instance, s.now()).Scan(&project)
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		j, err := s.Claim(selection, a, project)
		var f *failure
		if errors.As(err, &f) && f.code == "attempt_capacity" {
			continue
		}
		if err != nil {
			return out, err
		}
		out.Project = project
		attempt, cancel := context.WithTimeout(ctx, 15*time.Minute)
		err = s.runAttempt(attempt, a, &j, prerequisiteErr)
		cancel()
		if err != nil {
			code, transient := classify(err)
			// Save failure separately even if the operation's context expired.
			failureCtx, failureCancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
			saved := s.fail(failureCtx, a, j, code, transient)
			failureCancel()
			if saved != nil {
				return out, saved
			}
			out.State = "refused"
			if transient {
				out.State = "retry_wait"
			}
			out.Reason = code
			return out, nil
		}
		out.State = "adopted"
		// Successful scratch restores are reclaimed immediately; recovery pins
		// remain independent of scratch TTL and database/image rollback.
		if err = s.reconcileJob(ctx, a, project); err != nil {
			out.Reason = "cleanup_pending"
		}
		return out, nil
	}
	return out, discoveryErr
}

func (s *Service) runAttempt(ctx context.Context, a Authority, j *job, prerequisiteErr error) error {
	if !s.cfg.RecoveryReconciled {
		return ErrPrerequisite
	}
	// A reclaimed generation must preserve the crashed attempt's report before
	// publishing a fresh current payload, including checking-only crashes.
	if j.ReportRef != "" && s.reports != nil {
		old, err := s.reports.Get(ctx, j.ReportRef)
		if err != nil {
			return err
		}
		var prior Report
		if err = json.Unmarshal(old, &prior); err != nil {
			return err
		}
		if prior.Identity.Attempt != j.Identity.Attempt {
			if _, err = s.reports.RetainFailed(ctx, j.ReportRef); err != nil {
				return err
			}
			if err = s.checkpoint(ctx, a, *j, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET failed_report_ref=report_ref,failed_report_digest=report_digest,failed_attempt_id=$2,failure_summary='Previous attempt did not commit before its lease expired.',revision=revision+1,updated_at=$3 WHERE project_node_id=$1`, j.Identity.Project, prior.Identity.Attempt, s.now())
				return err
			}); err != nil {
				return err
			}
		}
	}
	if j.ReservedBytes > 0 || len(j.Journal.Operations) > 0 {
		if s.provider == nil {
			return ErrPrerequisite
		}
		if err := s.reconcileJob(ctx, a, j.Identity.Project); err != nil {
			return err
		}
		// Reconciliation is CAS-fenced and did not clear this new lease.
		err := s.with(ctx, a.Executor, func(tx pgx.Tx) error {
			var err error
			*j, err = s.loadJob(ctx, tx, a.Executor.TenantID, j.Identity.Project, false)
			return err
		})
		if err != nil {
			return err
		}
	}
	var report Report
	err := s.snapshot(ctx, a.Executor, func(tx pgx.Tx) error { var err error; report, err = s.plan(ctx, tx, j.Identity); return err })
	if err != nil {
		return err
	}
	if err = s.saveReport(ctx, a, j, report); err != nil {
		return err
	}
	if !report.Eligible || report.Incomplete {
		return &failure{code: "eligibility"}
	}
	if prerequisiteErr != nil {
		return ErrPrerequisite
	}
	err = s.checkpoint(ctx, a, *j, func(tx pgx.Tx) error { return authorize(ctx, tx, a, j.Identity.Project) })
	if err != nil {
		return err
	}
	if err = s.backup(ctx, a, j); err != nil {
		return err
	}
	_, err = s.apply(ctx, a, *j)
	return err
}
func classify(err error) (string, bool) {
	var f *failure
	if errors.As(err, &f) {
		return f.code, f.transient
	}
	if errors.Is(err, authz.ErrForbidden) {
		return "authority", false
	}
	if errors.Is(err, ErrPrerequisite) {
		return "rollout_dependency", true
	}
	if errors.Is(err, ErrStale) {
		return "stale_source", true
	}
	return "try_again", true
}

// Reconcile has reserved control capacity and a single instance leader. That
// session lock elects the sweeper; no project/tree/resource lock spans an RPC.
// Terminal jobs, refused jobs and expired generations remain in its inventory.
func (s *Service) Reconcile(ctx context.Context) error {
	if s.provider == nil || !s.cfg.RecoveryReconciled {
		return ErrPrerequisite
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	key := s.cfg.Instance + ":adoption-reconciliation"
	var leader bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,596))`, key).Scan(&leader); err != nil {
		return err
	}
	if !leader {
		return nil
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(release, `SELECT pg_advisory_unlock(hashtextextended($1,596))`, key); err != nil {
			_ = conn.Conn().Close(release)
		}
	}()
	var first error
	remaining := 100
	for _, a := range s.cfg.Authorities {
		if remaining <= 0 {
			break
		}
		projects := []string{}
		err = s.with(ctx, a.Executor, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT project_node_id::text FROM delivery_adoption_jobs WHERE instance_id=$1 AND resource_attempt_id IS NOT NULL AND coalesce(next_reconcile_at,'epoch')<=$2 AND (lease_until IS NULL OR lease_until<=$2 OR attempt_deadline_at<=$2 OR resource_attempt_id<>attempt_id) ORDER BY next_reconcile_at,project_node_id LIMIT $3`, s.cfg.Instance, s.now(), remaining)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				projects = append(projects, id)
			}
			err = rows.Err()
			rows.Close()
			return err
		})
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		remaining -= len(projects)
		for _, project := range projects {
			if err = s.reconcileJob(ctx, a, project); err != nil && !errors.Is(err, ErrLease) && first == nil {
				first = err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	if err = s.reconcileCatalog(ctx); err != nil && first == nil {
		first = err
	}
	return first
}
