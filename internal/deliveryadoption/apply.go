// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// apply has no network/storage effects. Every mapping is rebuilt inside its
// final fenced transaction; only that exact verified source can switch mode.
func (s *Service) apply(ctx context.Context, a Authority, j job) (bool, error) {
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, a.Executor), 120*time.Second)
	defer cancel()
	unchanged := false
	err := db.InTenant(ctx, s.pool, a.Executor.TenantID, func(tx pgx.Tx) error {
		if err := limits(ctx, tx, "60s"); err != nil {
			return err
		}
		// Importer uses its namespace key before pairing/tree. Never upgrade a
		// shared tree lock, nor acquire any importer/resource lock after events.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,42))`, a.Executor.TenantID+":relation-backfill"); err != nil {
			return err
		}
		if err := mutationFence(ctx, tx, a.Executor); err != nil {
			return err
		}
		if err := authorize(ctx, tx, a, j.Identity.Project); err != nil {
			return err
		}
		var adopted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_delivery WHERE project_node_id=$1)`, j.Identity.Project).Scan(&adopted); err != nil {
			return err
		}
		if adopted {
			unchanged = true
			return nil
		}
		current, err := s.loadJob(ctx, tx, a.Executor.TenantID, j.Identity.Project, false)
		if err != nil {
			return err
		}
		if current.Generation != j.Generation || current.Token != j.Token || current.Identity.Attempt != j.Identity.Attempt || !current.LeaseUntil.After(s.now()) || !current.Deadline.After(s.now()) || current.State != "applying" || current.Pin == "" || current.VerifiedAt == nil || current.ResourceAttempt != j.Identity.Attempt {
			return ErrLease
		}
		var evidence bool
		if err = tx.QueryRow(ctx, `SELECT evidence_attempt_id=attempt_id AND NOT report_incomplete AND report_ref IS NOT NULL AND instance_id=$2 AND rollout_artifact_ref=$3 AND rollout_authorization_ref=$4 AND executing_principal_id=$5 AND authorizing_principal_id=$6 FROM delivery_adoption_jobs WHERE project_node_id=$1`, j.Identity.Project, s.cfg.Instance, s.cfg.Artifact, a.Reference, a.Executor.ID, a.Authorizer.ID).Scan(&evidence); err != nil {
			return err
		}
		if !evidence {
			return ErrLease
		}
		protected := false
		for _, op := range current.Journal.Operations {
			if op.Kind == "pin" && op.Attempt == j.Identity.Attempt && op.Status == "complete" && op.Pin == current.Pin && op.Identity == current.Identity {
				protected = true
			}
		}
		if !protected {
			return ErrPrerequisite
		}
		r, err := s.plan(ctx, tx, current.Identity)
		if err != nil {
			return err
		}
		if !r.Eligible || r.Incomplete {
			return &failure{code: "eligibility"}
		}
		if r.Fingerprint != current.Fingerprint || r.Fingerprint != j.Fingerprint || current.ReportDigest != j.ReportDigest || current.Pin != j.Pin {
			return ErrStale
		}
		// Lock target nodes in one sorted batch before the final checkpoint row.
		ids := []string{j.Identity.Project}
		for _, v := range r.Releases {
			ids = append(ids, v.ID)
		}
		for _, v := range r.Members {
			ids = append(ids, v.ID)
		}
		sort.Strings(ids)
		rows, err := tx.Query(ctx, `SELECT id FROM nodes WHERE id=ANY($1::uuid[]) ORDER BY id FOR SHARE`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		current, err = s.loadJob(ctx, tx, a.Executor.TenantID, j.Identity.Project, true)
		if err != nil {
			return err
		}
		if current.Generation != j.Generation || current.Token != j.Token || !current.Deadline.After(s.now()) {
			return ErrLease
		}
		now := s.now()
		if _, err = tx.Exec(ctx, `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_at,adopted_by,next_sequence) VALUES($1,$2,$3,$4,$5)`, a.Executor.TenantID, j.Identity.Project, now, a.Executor.ID, r.NextSequence); err != nil {
			return err
		}
		changes := []events.Change{}
		for _, v := range r.Releases {
			if _, err = tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,visibility,sequence,state,rank,version_scheme,version,released_at,origin) VALUES($1,$2,$3,'published',$4,$5,$6,nullif($7,''),nullif($8,''),$9,$10)`, a.Executor.TenantID, j.Identity.Project, v.ID, v.Sequence, v.State, v.Rank, v.Scheme, v.Version, v.ReleasedAt, v.Origin); err != nil {
				return err
			}
			if v.Sequence != v.OriginalSequence {
				if v.DefaultTitle {
					if _, err = tx.Exec(ctx, `UPDATE nodes SET title=$2,updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1`, v.ID, fmt.Sprintf("Release %d", v.Sequence)); err != nil {
						return err
					}
				}
				id := v.ID
				changes = append(changes, events.Change{NodeID: &id, Type: "release.renumbered", Before: map[string]any{"sequence": v.OriginalSequence}, After: map[string]any{"sequence": v.Sequence, "origin": "adoption", "undo_available": false}})
			}
		}
		for _, v := range r.Members {
			var release any
			if v.Release != "" {
				release = v.Release
			}
			if _, err = tx.Exec(ctx, `INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,release_node_id,rank,source,placed_by,placed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, a.Executor.TenantID, j.Identity.Project, v.ID, release, v.Rank, v.Source, a.Executor.ID, now); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET state='adopted',adopted_at=$2,lease_token=NULL,lease_until=NULL,reason_code=NULL,reason_message='',cleanup_state='pending',next_reconcile_at=$2,revision=revision+1,updated_at=$2 WHERE project_node_id=$1`, j.Identity.Project, now); err != nil {
			return err
		}
		project := j.Identity.Project
		changes = append(changes, events.Change{NodeID: &project, Type: "delivery.adopted", After: map[string]any{"counts": r.Counts, "migration_version": Migration, "instance_id": s.cfg.Instance, "rollout_artifact_ref": s.cfg.Artifact, "rollout_authorization_ref": a.Reference, "attempt_id": j.Identity.Attempt, "report_ref": j.ReportRef, "report_digest": j.ReportDigest, "backup_ref": j.BackupRef, "backup_digest": j.BackupDigest, "recovery_pin_manifest_ref": j.Pin, "undo_available": false}})
		for _, change := range changes {
			if _, err = events.Append(ctx, tx, a.Executor, change); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	return unchanged, err
}

type Verification struct {
	Mode                      string `json:"mode"`
	JourneyMembers            int    `json:"journey_members"`
	AdoptedMembers            int    `json:"adopted_members"`
	MissingArchiveMembers     int    `json:"missing_archive_members"`
	PostAdoptionJourneyEvents int    `json:"post_adoption_journey_events"`
	OverCapacity              bool   `json:"over_capacity"`
	Incomplete                bool   `json:"incomplete"`
	OK                        bool   `json:"ok"`
}

// Verify is read-only evidence. It does not un-adopt, hide unexpected journey
// publications, or grant authority to boot a pre-E binary on an adopted DB.
func (s *Service) Verify(ctx context.Context, p tenant.Principal, project string) (Verification, error) {
	out := Verification{Mode: "journey"}
	if !uuidRE.MatchString(project) {
		return out, errors.New("invalid project")
	}
	err := s.snapshot(ctx, p, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var adoptedAt *time.Time
		if err := tx.QueryRow(ctx, `SELECT (SELECT adopted_at FROM project_delivery WHERE project_node_id=$1)`, project).Scan(&adoptedAt); err != nil {
			return err
		}
		if adoptedAt == nil {
			out.OK = true
			return nil
		}
		out.Mode = "releases"
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM journey_tickets WHERE project_node_id=$1 AND release_node_id IS NOT NULL LIMIT 5001) b`, project).Scan(&out.JourneyMembers); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM ships_in WHERE project_node_id=$1 AND source='adopted' LIMIT 5001) b`, project).Scan(&out.AdoptedMembers); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM journey_tickets j LEFT JOIN ships_in s ON s.tenant_id=j.tenant_id AND s.item_node_id=j.ticket_node_id WHERE j.project_node_id=$1 AND j.release_node_id IS NOT NULL AND (s.item_node_id IS NULL OR s.source<>'adopted') LIMIT 5001) b`, project).Scan(&out.MissingArchiveMembers); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM events WHERE at>$2 AND type LIKE 'journey.%' AND (node_id=$1 OR node_id IN (SELECT release_node_id FROM journey_releases WHERE project_node_id=$1)) LIMIT 1001) b`, project, adoptedAt).Scan(&out.PostAdoptionJourneyEvents); err != nil {
			return err
		}
		var releases, members int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM project_releases WHERE project_node_id=$1 AND state NOT IN ('released','abandoned') LIMIT 51) b`, project).Scan(&releases); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(n),0) FROM (SELECT (SELECT count(*) FROM (SELECT 1 FROM ships_in s WHERE s.project_node_id=$1 AND s.release_node_id=r.release_node_id LIMIT 1001) b) n FROM project_releases r WHERE r.project_node_id=$1 ORDER BY r.release_node_id LIMIT 201) b`, project).Scan(&members); err != nil {
			return err
		}
		out.OverCapacity = releases > 50 || members > 1000
		out.Incomplete = out.JourneyMembers > 5000 || out.AdoptedMembers > 5000 || out.PostAdoptionJourneyEvents > 1000
		out.OK = !out.Incomplete && !out.OverCapacity && out.MissingArchiveMembers == 0 && out.PostAdoptionJourneyEvents == 0
		return nil
	})
	return out, err
}
