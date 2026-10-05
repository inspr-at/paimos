// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

func (s *Service) prerequisites(ctx context.Context) error {
	q := s.cfg.Quota
	if s.provider == nil || s.reports == nil || s.cfg.RollbackFloor != s.cfg.Artifact || s.cfg.PreFirstAdoptionPin == "" || len(s.cfg.PreFirstAdoptionPin) > 512 || !s.cfg.ConsumersReady || !s.cfg.WritersStopped || !s.cfg.RecoveryReconciled || q.BundleBytes <= 0 || q.BundleBytes > 1<<50 || q.RestoreBytes <= 0 || q.RestoreBytes > 1<<50 || q.OperationSlots < 8 || q.OperationSlots > 64 || q.ActiveBytes < 2*q.BundleBytes || q.FailedBytes < 2*q.BundleBytes || q.FailedBytes/q.BundleBytes > 10000 || q.ProtectedBytes < q.BundleBytes {
		return ErrPrerequisite
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := s.provider.Capabilities(call)
	if err != nil {
		return ErrPrerequisite
	}
	if !c.DurableLookup || !c.Idempotency || !c.Cancellation || !c.HardExpiry || !c.QuotaSeparation || !c.RecoveryCatalog || !c.ReferenceAwareRetirement || !c.IsolatedRestore {
		return ErrPrerequisite
	}
	proof, err := s.provider.Lookup(call, Operation{Key: s.cfg.PreFirstAdoptionPin, Kind: "pre-first-adoption", Identity: Identity{Instance: s.cfg.Instance, Migration: Migration}})
	if err != nil || proof.State != "complete" || proof.Identity.Instance != s.cfg.Instance || proof.Pin != s.cfg.PreFirstAdoptionPin || !proof.Protected || !proof.IntegrityVerified || !proof.ChainVerified || !proof.Restored || !proof.Unadopted {
		return ErrPrerequisite
	}
	return nil
}

// reserve accounts for worst-case orphan cost in the failed pool before any
// acquisition. Active capacity cannot be borrowed by failed/uncertain bundles.
// This lock is never held during provider work or the final adoption write.
func (s *Service) reserve(ctx context.Context, a Authority, j *job) error {
	q := s.cfg.Quota
	return db.InTransaction(ctx, s.pool, func(group context.Context) error {
		if err := db.InTenant(tenantContext(group, a), s.pool, a.Executor.TenantID, func(tx pgx.Tx) error {
			if err := limits(group, tx, "15s"); err != nil {
				return err
			}
			_, err := tx.Exec(group, `SELECT pg_advisory_xact_lock(hashtextextended($1,596))`, s.cfg.Instance+":adoption-admission")
			return err
		}); err != nil {
			return err
		}
		var held, active int64
		for _, other := range s.cfg.Authorities {
			err := db.InTenant(tenantContext(group, other), s.pool, other.Executor.TenantID, func(tx pgx.Tx) error {
				var h, b int64
				// The configured failed pool bounds this population before summing.
				bound := min(int64(10001), q.FailedBytes/q.BundleBytes+1)
				if err := tx.QueryRow(group, `SELECT coalesce(sum(reserved_backup_bytes),0),coalesce(sum(reserved_backup_bytes) FILTER(WHERE lease_until>$2),0) FROM (SELECT reserved_backup_bytes,lease_until FROM delivery_adoption_jobs WHERE instance_id=$1 AND reserved_backup_bytes>0 ORDER BY project_node_id LIMIT $3) bounded`, s.cfg.Instance, s.now(), bound).Scan(&h, &b); err != nil {
					return err
				}
				held += h
				active += b
				return nil
			})
			if err != nil {
				return err
			}
			if held+q.BundleBytes > q.FailedBytes || active+q.BundleBytes > q.ActiveBytes {
				return capacityError("backup_capacity")
			}
		}
		return db.InTenant(tenantContext(group, a), s.pool, a.Executor.TenantID, func(tx pgx.Tx) error {
			if err := mutationFence(group, tx, a.Executor); err != nil {
				return err
			}
			if err := authorize(group, tx, a, j.Identity.Project); err != nil {
				return err
			}
			current, err := s.loadJob(group, tx, a.Executor.TenantID, j.Identity.Project, true)
			if err != nil {
				return err
			}
			if current.Generation != j.Generation || current.Token != j.Token || !current.Deadline.After(s.now()) || !current.LeaseUntil.After(s.now()) {
				return ErrLease
			}
			if current.ReservedBytes > 0 || len(current.Journal.Operations) > 0 {
				return capacityError("cleanup_pending")
			}
			j.ResourceAttempt = j.Identity.Attempt
			j.ReservedBytes = q.BundleBytes
			j.BackupRef = ""
			j.BackupDigest = ""
			j.RestoreRef = ""
			j.RestoreDigest = ""
			j.Pin = ""
			j.VerifiedAt = nil
			_, err = tx.Exec(group, `UPDATE delivery_adoption_jobs SET state='backing_up',backing_up_at=$2,resource_attempt_id=$3,reserved_backup_bytes=$4,reserved_restore_slots=1,restore_expires_at=$5,failed_resources_expires_at=$6,cleanup_state='none',next_reconcile_at=$2,revision=revision+1,updated_at=$2 WHERE project_node_id=$1`, j.Identity.Project, s.now(), j.Identity.Attempt, q.BundleBytes, s.now().Add(30*time.Minute), s.now().Add(24*time.Hour))
			return err
		})
	})
}

func operationKey(identity Identity, kind string) string {
	raw, _ := json.Marshal(struct {
		Identity Identity
		Kind     string
	}{identity, kind})
	return sum(raw)
}
func (s *Service) intent(ctx context.Context, a Authority, j *job, kind string) (Operation, error) {
	id := j.Identity
	if j.ResourceAttempt != "" {
		id.Attempt = j.ResourceAttempt
	}
	key := operationKey(id, kind)
	for _, op := range j.Journal.Operations {
		if op.Key == key {
			return op, nil
		}
	}
	if len(j.Journal.Operations) >= 8 {
		return Operation{}, errors.New("provider journal is full")
	}
	op := Operation{Key: key, Attempt: id.Attempt, Kind: kind, Status: "intent", Identity: id, Bytes: j.ReservedBytes, Slots: 1, Deadline: s.now().Add(10 * time.Minute)}
	next := Journal{Operations: append(append([]Operation(nil), j.Journal.Operations...), op)}
	raw, err := json.Marshal(next)
	if err != nil {
		return op, err
	}
	if err = decodeJournal(raw, &next); err != nil {
		return op, err
	}
	err = s.checkpoint(ctx, a, *j, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET operation_journal=$2,revision=revision+1,updated_at=$3 WHERE project_node_id=$1`, j.Identity.Project, raw, s.now())
		return err
	})
	if err == nil {
		j.Journal = next
	}
	return op, err
}
func (s *Service) recordResult(ctx context.Context, a Authority, j *job, op Operation, result ProviderResult) error {
	if err := validResult(op, result); err != nil {
		return err
	}
	for i := range j.Journal.Operations {
		if j.Journal.Operations[i].Key == op.Key {
			j.Journal.Operations[i].Status = result.State
			j.Journal.Operations[i].Handle = result.Handle
			j.Journal.Operations[i].Pin = result.Pin
		}
	}
	raw, err := json.Marshal(j.Journal)
	if err != nil {
		return err
	}
	if err = decodeJournal(raw, &Journal{}); err != nil {
		return err
	}
	return s.checkpoint(ctx, a, *j, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET operation_journal=$2,revision=revision+1,updated_at=$3 WHERE project_node_id=$1`, j.Identity.Project, raw, s.now())
		return err
	})
}
func validResult(op Operation, v ProviderResult) error {
	if v.Key != op.Key || v.Kind != op.Kind || v.Identity != op.Identity || len(v.Handle) > 512 || len(v.BackupRef) > 512 || len(v.RestoreRef) > 512 || len(v.Pin) > 512 || len(v.BackupDigest) > 64 || len(v.RestoreDigest) > 64 || len(v.Fingerprint) > 64 || v.Bytes < 0 {
		return errors.New("provider result identity or bounds mismatch")
	}
	switch v.State {
	case "complete", "pending", "missing", "reclaimed":
	default:
		return errors.New("invalid provider result state")
	}
	return nil
}

// effect persists intent, then looks up the original key before execution.
// Retrying a crash between provider success and result CAS never double allocates.
func (s *Service) effect(ctx context.Context, a Authority, j *job, kind string) (ProviderResult, error) {
	op, err := s.intent(ctx, a, j, kind)
	if err != nil {
		return ProviderResult{}, err
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	v, err := s.provider.Lookup(call, op)
	cancel()
	if err != nil {
		return v, capacityError("backup_failed")
	}
	if err = validResult(op, v); err != nil {
		return v, err
	}
	if v.State == "missing" {
		call, cancel = context.WithTimeout(ctx, 5*time.Second)
		v, err = s.provider.Execute(call, ProviderRequest{Operation: op, Fingerprint: j.Fingerprint, Backup: j.BackupRef, Pin: j.Pin, Quota: s.cfg.Quota, RestoreExpiry: s.now().Add(30 * time.Minute), PayloadExpiry: s.now().Add(24 * time.Hour)})
		cancel()
		if err != nil {
			return v, capacityError("backup_failed")
		}
	}
	if err = s.recordResult(ctx, a, j, op, v); err != nil {
		return v, err
	}
	// Providers return a durable pending operation quickly. Poll the same key;
	// no second Execute or unjournalled allocation is permitted. The ten-minute
	// backup context and injected clock bound the entire backup/restore bundle.
	for polls := 0; v.State == "pending" && polls < 600; polls++ {
		if !op.Deadline.After(s.now()) {
			return v, context.DeadlineExceeded
		}
		if err = s.wait(ctx); err != nil {
			return v, err
		}
		call, cancel = context.WithTimeout(ctx, 5*time.Second)
		v, err = s.provider.Lookup(call, op)
		cancel()
		if err != nil {
			return v, capacityError("backup_failed")
		}
		if err = validResult(op, v); err != nil {
			return v, err
		}
		if v.State != "pending" {
			if err = s.recordResult(ctx, a, j, op, v); err != nil {
				return v, err
			}
		}
	}
	if v.State != "complete" {
		return v, capacityError("backup_failed")
	}
	if v.Bytes > s.cfg.Quota.BundleBytes {
		return v, capacityError("backup_capacity")
	}
	return v, nil
}

func (s *Service) backup(ctx context.Context, a Authority, j *job) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := s.reserve(ctx, a, j); err != nil {
		return err
	}
	v, err := s.effect(ctx, a, j, "backup")
	if err != nil {
		return err
	}
	if v.BackupRef == "" || !digestRE.MatchString(v.BackupDigest) || !v.IntegrityVerified || !v.ChainVerified {
		return capacityError("backup_failed")
	}
	j.BackupRef = v.BackupRef
	j.BackupDigest = v.BackupDigest
	v, err = s.effect(ctx, a, j, "restore")
	if err != nil {
		return err
	}
	if !v.Restored || !v.IntegrityVerified || !v.ChainVerified || v.Fingerprint != j.Fingerprint || v.BackupRef != j.BackupRef || v.BackupDigest != j.BackupDigest || v.RestoreRef == "" || !digestRE.MatchString(v.RestoreDigest) {
		return capacityError("backup_failed")
	}
	j.RestoreRef = v.RestoreRef
	j.RestoreDigest = v.RestoreDigest
	v, err = s.effect(ctx, a, j, "pin")
	if err != nil {
		return err
	}
	if !v.Protected || !v.ChainVerified || !v.IntegrityVerified || !v.Restored || v.Pin == "" || v.Fingerprint != j.Fingerprint || v.BackupRef != j.BackupRef || v.BackupDigest != j.BackupDigest || v.RestoreRef != j.RestoreRef || v.RestoreDigest != j.RestoreDigest {
		return capacityError("backup_failed")
	}
	j.Pin = v.Pin
	now := s.now()
	j.VerifiedAt = &now
	return s.checkpoint(ctx, a, *j, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET state='applying',applying_at=$2,backup_ref=$3,backup_digest=$4,restore_evidence_ref=$5,restore_evidence_digest=$6,recovery_pin_manifest_ref=$7,backup_verified_at=$2,evidence_attempt_id=attempt_id,revision=revision+1,updated_at=$2 WHERE project_node_id=$1`, j.Identity.Project, now, j.BackupRef, j.BackupDigest, j.RestoreRef, j.RestoreDigest, j.Pin)
		return err
	})
}

// Cleanup must be an idempotent, durable provider operation too. It is performed
// after fenced database inspection and outside all mutation locks. A database
// restore erasing that inspection is handled conservatively by catalog sweep.
func (s *Service) reconcileJob(ctx context.Context, a Authority, project string) error {
	if s.provider == nil || !s.cfg.RecoveryReconciled {
		return ErrPrerequisite
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var j job
	var adopted, live bool
	var revision int64
	err := s.with(ctx, a.Executor, func(tx pgx.Tx) error {
		if err := mutationFence(ctx, tx, a.Executor); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, a.Executor, "releases.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var err error
		j, err = s.loadJob(ctx, tx, a.Executor.TenantID, project, true)
		if err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_delivery WHERE project_node_id=$1),coalesce(lease_until>$2 AND attempt_deadline_at>$2,false),revision FROM delivery_adoption_jobs WHERE project_node_id=$1`, project, s.now()).Scan(&adopted, &live, &revision); err != nil {
			return err
		}
		if j.recoveryUnknown() {
			return capacityError("cleanup_blocked")
		}
		if j.ResourceAttempt == "" {
			return nil
		}
		// A current live attempt may still commit its pin. Never clean it.
		if live && j.ResourceAttempt == j.Identity.Attempt {
			return ErrLease
		}
		id := j.Identity
		id.Attempt = j.ResourceAttempt
		for _, op := range j.Journal.Operations {
			if op.Attempt == id.Attempt {
				id = op.Identity
				break
			}
		}
		for _, kind := range []string{"cancel", "cleanup"} {
			key := operationKey(id, kind)
			exists := false
			for _, op := range j.Journal.Operations {
				if op.Key == key {
					exists = true
				}
			}
			if !exists {
				j.Journal.Operations = append(j.Journal.Operations, Operation{Key: key, Attempt: id.Attempt, Kind: kind, Status: "intent", Identity: id, Deadline: s.now().Add(time.Minute), Pin: j.Pin})
			}
		}
		raw, err := json.Marshal(j.Journal)
		if err != nil {
			return err
		}
		if err = decodeJournal(raw, &Journal{}); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET cleanup_state='checking',operation_journal=$2,revision=revision+1,updated_at=$3 WHERE project_node_id=$1`, project, raw, s.now())
		revision++
		return err
	})
	if err != nil || j.ResourceAttempt == "" {
		return err
	}
	confirmed := true
	// Discover unknown successes by OLD immutable operation keys before cleanup.
	for _, op := range j.Journal.Operations {
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		v, e := s.provider.Lookup(call, op)
		cancel()
		if e != nil || validResult(op, v) != nil {
			confirmed = false
			break
		}
		if op.Kind != "cancel" && op.Kind != "cleanup" {
			if v.Pin != "" {
				j.Pin = v.Pin
			}
			continue
		}
		if v.State != "complete" && v.State != "reclaimed" {
			call, cancel = context.WithTimeout(ctx, 5*time.Second)
			// A successful pin is retained forever by this API. For a failed,
			// fenced generation, reference-aware cleanup may retire a provisional
			// pin only when the provider catalog proves no recovery references.
			pin := j.Pin
			if adopted {
				pin = ""
			}
			v, e = s.provider.Execute(call, ProviderRequest{Operation: op, Backup: j.BackupRef, Pin: pin, Quota: s.cfg.Quota})
			cancel()
			if e != nil || validResult(op, v) != nil {
				confirmed = false
				break
			}
		}
		if v.State != "complete" && v.State != "reclaimed" {
			confirmed = false
			break
		}
	}
	err = s.with(ctx, a.Executor, func(tx pgx.Tx) error {
		if err := mutationFence(ctx, tx, a.Executor); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, a.Executor, "releases.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		current, err := s.loadJob(ctx, tx, a.Executor.TenantID, project, true)
		if err != nil {
			return err
		}
		var actualRevision int64
		if err = tx.QueryRow(ctx, `SELECT revision FROM delivery_adoption_jobs WHERE project_node_id=$1`, project).Scan(&actualRevision); err != nil {
			return err
		}
		if actualRevision != revision || current.ResourceAttempt != j.ResourceAttempt {
			return ErrLease
		}
		if confirmed {
			_, err = tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET reserved_backup_bytes=0,reserved_restore_slots=0,resource_attempt_id=NULL,operation_journal='{"operations":[]}',cleanup_state='reclaimed',next_reconcile_at=NULL,reconciliation_cursor=NULL,revision=revision+1,updated_at=$2 WHERE project_node_id=$1`, project, s.now())
		} else {
			_, err = tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET cleanup_state='blocked',reason_code='cleanup_blocked',reason_message=$2,cleanup_attempts=cleanup_attempts+1,next_reconcile_at=$3,revision=revision+1,updated_at=$4 WHERE project_node_id=$1`, project, safeReason("cleanup_blocked"), s.now().Add(retryDelay(j.CleanupAttempts+1)), s.now())
		}
		return err
	})
	if err != nil {
		return err
	}
	if !confirmed {
		return capacityError("cleanup_blocked")
	}
	return nil
}
