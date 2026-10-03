// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type job struct {
	Identity                                                Identity
	Token                                                   string
	Generation                                              int64
	Deadline, LeaseUntil                                    time.Time
	Attempts                                                int
	State                                                   string
	Fingerprint, ReportRef, ReportDigest                    string
	BackupRef, BackupDigest, RestoreRef, RestoreDigest, Pin string
	VerifiedAt                                              *time.Time
	ResourceAttempt                                         string
	ReservedBytes                                           int64
	Cleanup                                                 string
	CleanupAttempts                                         int
	Journal                                                 Journal
}

const jobColumns = `project_node_id::text,coalesce(attempt_id::text,''),coalesce(lease_token::text,''),lease_generation,coalesce(attempt_deadline_at,'epoch'),coalesce(lease_until,'epoch'),attempts,state,coalesce(source_fingerprint,''),coalesce(report_ref,''),coalesce(report_digest,''),coalesce(backup_ref,''),coalesce(backup_digest,''),coalesce(restore_evidence_ref,''),coalesce(restore_evidence_digest,''),coalesce(recovery_pin_manifest_ref,''),backup_verified_at,coalesce(resource_attempt_id::text,''),reserved_backup_bytes,cleanup_state,cleanup_attempts,operation_journal`

func (s *Service) loadJob(ctx context.Context, tx pgx.Tx, tenantID, project string, locked bool) (job, error) {
	var j job
	var raw []byte
	var attempt string
	q := `SELECT ` + jobColumns + ` FROM delivery_adoption_jobs WHERE project_node_id=$1 AND instance_id=$2`
	if locked {
		q += ` FOR NO KEY UPDATE`
	}
	err := tx.QueryRow(ctx, q, project, s.cfg.Instance).Scan(&project, &attempt, &j.Token, &j.Generation, &j.Deadline, &j.LeaseUntil, &j.Attempts, &j.State, &j.Fingerprint, &j.ReportRef, &j.ReportDigest, &j.BackupRef, &j.BackupDigest, &j.RestoreRef, &j.RestoreDigest, &j.Pin, &j.VerifiedAt, &j.ResourceAttempt, &j.ReservedBytes, &j.Cleanup, &j.CleanupAttempts, &raw)
	j.Identity = s.identity(tenantID, project, attempt)
	j.Identity.Generation = j.Generation
	if err == nil {
		err = decodeJournal(raw, &j.Journal)
		for _, op := range j.Journal.Operations {
			if op.Identity.Instance != s.cfg.Instance || op.Identity.Tenant != tenantID || op.Identity.Project != project {
				return j, errors.New("journal ownership does not match project")
			}
		}
	}
	return j, err
}
func decodeJournal(raw []byte, j *Journal) error {
	if len(raw) > 16384 {
		return errors.New("adoption journal exceeds bound")
	}
	if err := strictJSON(raw, j); err != nil {
		return err
	}
	if len(j.Operations) > 8 {
		return errors.New("adoption journal exceeds operation bound")
	}
	for _, op := range j.Operations {
		if !digestRE.MatchString(op.Key) || !uuidRE.MatchString(op.Attempt) || !uuidRE.MatchString(op.Identity.Tenant) || !uuidRE.MatchString(op.Identity.Project) || op.Identity.Instance == "" || len(op.Identity.Instance) > 128 || op.Identity.Generation < 1 || op.Key != operationKey(op.Identity, op.Kind) || op.Bytes < 0 || op.Bytes > 1<<50 || op.Slots < 0 || op.Slots > 1 || op.Deadline.IsZero() || len(op.Handle) > 512 || len(op.Pin) > 512 || op.Identity.Attempt != op.Attempt || op.Identity.Migration != Migration {
			return errors.New("invalid durable operation identity")
		}
		switch op.Kind {
		case "backup", "restore", "pin", "cancel", "cleanup":
		default:
			return errors.New("invalid durable operation kind")
		}
		switch op.Status {
		case "intent", "pending", "missing", "complete", "reclaimed":
		default:
			return errors.New("invalid durable operation status")
		}
	}
	return nil
}
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func retryDelay(attempts int) time.Duration {
	switch attempts {
	case 0, 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 15 * time.Minute
	default:
		return time.Hour
	}
}

// Claim's instance admission lock exists only in short job transactions. Apply
// never takes it under resource/tree locks. Expired leases release scheduling
// capacity, while durable resource reservations remain charged across attempts.
func (s *Service) Claim(ctx context.Context, a Authority, project string) (job, error) {
	var out job
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	err := db.InTransaction(ctx, s.pool, func(group context.Context) error {
		if err := db.InTenant(tenantContext(group, a), s.pool, a.Executor.TenantID, func(tx pgx.Tx) error {
			if err := limits(group, tx, "15s"); err != nil {
				return err
			}
			_, err := tx.Exec(group, `SELECT pg_advisory_xact_lock(hashtextextended($1,596))`, s.cfg.Instance+":adoption-admission")
			return err
		}); err != nil {
			return err
		}
		active := 0
		tenantActive := 0
		for _, other := range s.cfg.Authorities {
			if err := db.InTenant(tenantContext(group, other), s.pool, other.Executor.TenantID, func(tx pgx.Tx) error {
				var n int
				if err := tx.QueryRow(group, `SELECT count(*) FROM (SELECT 1 FROM delivery_adoption_jobs WHERE instance_id=$1 AND lease_until>$2 LIMIT 3) bounded`, s.cfg.Instance, s.now()).Scan(&n); err != nil {
					return err
				}
				active += n
				if other.Executor.TenantID == a.Executor.TenantID {
					tenantActive = n
				}
				return nil
			}); err != nil {
				return err
			}
			if active >= 2 {
				break
			}
		}
		if active >= 2 || tenantActive > 0 {
			return capacityError("attempt_capacity")
		}
		return db.InTenant(tenantContext(group, a), s.pool, a.Executor.TenantID, func(tx pgx.Tx) error {
			if err := mutationFence(group, tx, a.Executor); err != nil {
				return err
			}
			if err := authz.RequireTx(group, tx, a.Executor, "releases.read", authz.Scope{ProjectID: project}); err != nil {
				return err
			}
			j, err := s.loadJob(group, tx, a.Executor.TenantID, project, true)
			if err != nil {
				return err
			}
			if j.State == "adopted" || j.LeaseUntil.After(s.now()) {
				return ErrLease
			}
			var ready bool
			if err = tx.QueryRow(group, `SELECT next_attempt_at<=$2 AND instance_id=$3 AND rollout_artifact_ref=$4 AND executing_principal_id=$5 AND authorizing_principal_id=$6 AND rollout_authorization_ref=$7 FROM delivery_adoption_jobs WHERE project_node_id=$1`, project, s.now(), s.cfg.Instance, s.cfg.Artifact, a.Executor.ID, a.Authorizer.ID, a.Reference).Scan(&ready); err != nil {
				return err
			}
			if !ready {
				return ErrLease
			}
			// Do not clear any old operation key, handle or reservation here.
			out = j
			out.Identity.Attempt = newUUID()
			out.Token = newUUID()
			out.Generation++
			out.Identity.Generation = out.Generation
			out.Attempts++
			out.State = "checking"
			out.Deadline = s.now().Add(15 * time.Minute)
			out.LeaseUntil = s.now().Add(16 * time.Minute)
			_, err = tx.Exec(group, `UPDATE delivery_adoption_jobs SET state='checking',attempt_id=$2,lease_token=$3,lease_generation=$4,attempts=attempts+1,attempt_started_at=$5,attempt_deadline_at=$6,lease_until=$7,checking_at=$5,backup_verified_at=NULL,evidence_attempt_id=NULL,revision=revision+1,updated_at=$5 WHERE project_node_id=$1`, project, out.Identity.Attempt, out.Token, out.Generation, s.now(), out.Deadline, out.LeaseUntil)
			return err
		})
	})
	return out, err
}

func tenantContext(ctx context.Context, a Authority) context.Context {
	return tenant.WithPrincipal(ctx, a.Executor)
}

func capacityError(code string) error { return &failure{code: code, transient: true} }

type failure struct {
	code      string
	transient bool
}

func (f *failure) Error() string { return f.code }

// checkpoint checks both generation and hard deadlines using the injected
// clock. A late provider result cannot turn a reclaimed lease into authority.
func (s *Service) checkpoint(ctx context.Context, a Authority, j job, fn func(pgx.Tx) error) error {
	return s.checkpointDeadline(ctx, a, j, true, fn)
}
func (s *Service) checkpointDeadline(ctx context.Context, a Authority, j job, deadline bool, fn func(pgx.Tx) error) error {
	return s.with(ctx, a.Executor, func(tx pgx.Tx) error {
		if err := mutationFence(ctx, tx, a.Executor); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, a.Executor, "releases.read", authz.Scope{ProjectID: j.Identity.Project}); err != nil {
			return err
		}
		var live bool
		if err := tx.QueryRow(ctx, `SELECT coalesce(lease_token=$2::uuid AND lease_generation=$3 AND lease_until>$4 AND (NOT $5 OR attempt_deadline_at>$4) AND state<>'adopted',false) FROM delivery_adoption_jobs WHERE project_node_id=$1 FOR NO KEY UPDATE`, j.Identity.Project, j.Token, j.Generation, s.now(), deadline).Scan(&live); err != nil {
			return err
		}
		if !live {
			return ErrLease
		}
		return fn(tx)
	})
}

func (s *Service) saveReport(ctx context.Context, a Authority, j *job, r Report) error {
	if s.reports == nil {
		return ErrPrerequisite
	}
	if len(r.Releases) > 200 || len(r.Members) > 5000 || len(r.Reasons) > 5200 {
		return errors.New("dry-run report exceeds bounded item counts")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(raw) > MaxReportBytes {
		return errors.New("dry-run report exceeds bounded metadata size")
	}
	ref, err := s.reports.Put(ctx, j.Identity, raw)
	if err != nil {
		return err
	}
	if len(ref) < 1 || len(ref) > 512 {
		return errors.New("invalid protected report reference")
	}
	digest := sum(raw)
	counts, err := json.Marshal(r.Counts)
	if err != nil {
		return err
	}
	err = s.checkpoint(ctx, a, *j, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET source_fingerprint=nullif($2,''),report_ref=$3,report_digest=$4,report_counts=$5,report_incomplete=$6,last_checked_at=$7,revision=revision+1,updated_at=$7 WHERE project_node_id=$1`, j.Identity.Project, r.Fingerprint, ref, digest, counts, r.Incomplete, s.now())
		return err
	})
	if err == nil {
		j.Fingerprint = r.Fingerprint
		j.ReportRef = ref
		j.ReportDigest = digest
	}
	return err
}

// fail closes this attempt before any cleanup can retire its provisional pin.
// Failure diagnostics are fixed safe messages; raw provider stderr/errors are
// never stored in the job or exposed on the release page.
func (s *Service) fail(ctx context.Context, a Authority, j job, code string, transient bool) error {
	state := "refused"
	next := s.now().Add(24 * time.Hour)
	if transient {
		state = "retry_wait"
		next = s.now().Add(retryDelay(j.Attempts))
	}
	failedRef := j.ReportRef
	if failedRef != "" && s.reports != nil {
		var err error
		failedRef, err = s.reports.RetainFailed(ctx, failedRef)
		if err != nil {
			return err
		}
	}
	return s.checkpointDeadline(ctx, a, j, false, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET state=$2,reason_code=$3,reason_message=$4,failure_summary=$4,failed_report_ref=report_ref,failed_report_digest=report_digest,failed_attempt_id=attempt_id,refused_at=CASE WHEN $2='refused' THEN $5 ELSE refused_at END,
		 next_attempt_at=$6,lease_token=NULL,lease_until=NULL,cleanup_state=CASE WHEN reserved_backup_bytes>0 OR jsonb_array_length(operation_journal->'operations')>0 THEN 'pending' ELSE cleanup_state END,next_reconcile_at=CASE WHEN resource_attempt_id IS NOT NULL THEN $5 ELSE next_reconcile_at END,revision=revision+1,updated_at=$5 WHERE project_node_id=$1`, j.Identity.Project, state, code, safeReason(code), s.now(), next)
		if err != nil {
			return err
		}
		if failedRef != "" {
			_, err = tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET failed_report_ref=$2 WHERE project_node_id=$1`, j.Identity.Project, failedRef)
		}
		return err
	})
}
func safeReason(code string) string {
	switch code {
	case "backup_capacity", "attempt_capacity":
		return "Backup or restore capacity is unavailable; reconciliation precedes fresh allocation."
	case "backup_failed":
		return "Backup acquisition or isolated restore verification failed. The project remains in journey mode."
	case "cleanup_pending", "cleanup_blocked":
		return "Owned backup resources have not been confirmed reclaimed; no fresh bundle is allocated."
	case "stale_source":
		return "Project inputs changed since the recovery point. A fresh report and verified backup are required."
	case "authority":
		return "The recorded instance-local rollout authority is unavailable or revoked. Repair its project permissions."
	case "rollout_dependency":
		return "Instance release, backup-provider or pre-first-adoption recovery prerequisites are incomplete."
	case "eligibility":
		return "This project is not eligible. Read the persisted report for the offending rows and repairs."
	default:
		return "Adoption did not complete; the project remains in journey mode and will be checked again."
	}
}
