// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// AdoptionReporting is the P3/P4a boundary. P3 owns protected report storage,
// instance rollout configuration and waking the existing automatic job. RequestTx
// runs under the canonical access fences and must compare the job revision; it
// neither applies mappings nor performs provider I/O. ReadReport is authorized
// by P4a and must return a bounded, persisted page, never a fresh dry run.
type AdoptionReporting interface {
	ReadReport(context.Context, tenant.Principal, string, string, int) (AdoptionReport, error)
	RequestTx(context.Context, pgx.Tx, tenant.Principal, string, string, int64) (AdoptionJob, error)
}
type AdoptionReport struct {
	Items             []json.RawMessage `json:"items"`
	NextCursor        string            `json:"next_cursor,omitempty"`
	Incomplete        bool              `json:"incomplete"`
	Revision          int64             `json:"revision"`
	SourceFingerprint string            `json:"source_fingerprint,omitempty"`
}
type AdoptionJob struct {
	State                string          `json:"state"`
	Revision             int64           `json:"revision"`
	Attempts             int             `json:"attempts"`
	ReasonCode           string          `json:"reason_code,omitempty"`
	ReasonMessage        string          `json:"reason_message,omitempty"`
	LastCheckedAt        *time.Time      `json:"last_checked_at"`
	NextAttemptAt        *time.Time      `json:"next_attempt_at"`
	LeaseUntil           *time.Time      `json:"lease_until"`
	ReportRef            string          `json:"report_ref,omitempty"`
	ReportDigest         string          `json:"report_digest,omitempty"`
	ReportCounts         json.RawMessage `json:"report_counts"`
	ReportIncomplete     bool            `json:"report_incomplete"`
	BackupRef            string          `json:"backup_ref,omitempty"`
	BackupVerifiedAt     *time.Time      `json:"backup_verified_at"`
	CleanupState         string          `json:"cleanup_state"`
	NextReconcileAt      *time.Time      `json:"next_reconcile_at"`
	ReservedBackupBytes  int64           `json:"reserved_backup_bytes"`
	ReservedRestoreSlots int             `json:"reserved_restore_slots"`
}
type DeliveryStatus struct {
	ProjectID     string        `json:"project_id"`
	Title         string        `json:"title,omitempty"`
	Mode          string        `json:"mode"`
	Revision      int64         `json:"revision"`
	NextSequence  int           `json:"next_sequence,omitempty"`
	BuildDefaults BuildSettings `json:"build_defaults"`
	Adoption      *AdoptionJob  `json:"adoption"`
}

const adoptionColumns = `j.state,j.revision,j.attempts,coalesce(j.reason_code,''),j.reason_message,j.last_checked_at,j.next_attempt_at,j.lease_until,coalesce(j.report_ref,''),coalesce(j.report_digest,''),j.report_counts,j.report_incomplete,coalesce(j.backup_ref,''),j.backup_verified_at,j.cleanup_state,j.next_reconcile_at,j.reserved_backup_bytes,j.reserved_restore_slots`

func scanJob(row pgx.Row) (AdoptionJob, error) {
	var j AdoptionJob
	err := row.Scan(&j.State, &j.Revision, &j.Attempts, &j.ReasonCode, &j.ReasonMessage, &j.LastCheckedAt, &j.NextAttemptAt, &j.LeaseUntil, &j.ReportRef, &j.ReportDigest, &j.ReportCounts, &j.ReportIncomplete, &j.BackupRef, &j.BackupVerifiedAt, &j.CleanupState, &j.NextReconcileAt, &j.ReservedBackupBytes, &j.ReservedRestoreSlots)
	return j, err
}
func statusTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) (DeliveryStatus, error) {
	out := DeliveryStatus{ProjectID: project, Mode: "journey"}
	var defaults []byte
	err := tx.QueryRow(ctx, `SELECT n.title,coalesce(d.revision,0),coalesce(d.next_sequence,0),coalesce(d.build_defaults,'{}'::jsonb),d.project_node_id IS NOT NULL FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id AND k.slug='project' LEFT JOIN project_delivery d ON d.tenant_id=n.tenant_id AND d.project_node_id=n.id WHERE n.tenant_id=$1 AND n.id=$2 AND n.deleted_at IS NULL`, p.TenantID, project).Scan(&out.Title, &out.Revision, &out.NextSequence, &defaults, new(bool))
	if err != nil {
		return out, err
	}
	if out.Revision > 0 {
		out.Mode = "releases"
	}
	out.BuildDefaults, err = ParseBuildSettings(defaults)
	if err != nil {
		return out, err
	}
	job, err := scanJob(tx.QueryRow(ctx, `SELECT `+adoptionColumns+` FROM delivery_adoption_jobs j WHERE j.tenant_id=$1 AND j.project_node_id=$2`, p.TenantID, project))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Adoption = &job
	return out, nil
}
func (s *Store) Status(ctx context.Context, p tenant.Principal, project string) (DeliveryStatus, error) {
	var out DeliveryStatus
	err := s.read(ctx, p, project, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = statusTx(ctx, tx, p, project)
		return err
	})
	return out, err
}

type AdoptionPage struct {
	Items             []DeliveryStatus `json:"items"`
	NextCursor        string           `json:"next_cursor,omitempty"`
	Counts            map[string]int   `json:"counts"`
	CountsComplete    bool             `json:"counts_complete"`
	DiscoveryComplete bool             `json:"discovery_complete"`
	Status            string           `json:"status"`
}

func (s *Store) Adoptions(ctx context.Context, p tenant.Principal, opt ReadOptions) (AdoptionPage, error) {
	out := AdoptionPage{Items: []DeliveryStatus{}, Counts: map[string]int{}, Status: "running"}
	limit, err := pageLimit(opt.Limit, 50)
	if err != nil {
		return out, err
	}
	scope := p.TenantID + ":adoptions:" + opt.State
	c, err := decodeCursor(opt.Cursor, scope)
	if err != nil {
		return out, err
	}
	switch opt.State {
	case "", "pending", "checking", "backing_up", "applying", "adopted", "refused", "retry_wait":
	default:
		return out, errors.New("invalid adoption state")
	}
	err = s.read(ctx, p, "", func(ctx context.Context, tx pgx.Tx) error {
		// Check each project's actual releases.read grant in SQL before counts or
		// pagination; project visibility by itself is not release-read authority.
		inventory := `FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id AND k.slug='project' LEFT JOIN delivery_adoption_jobs j ON j.tenant_id=n.tenant_id AND j.project_node_id=n.id LEFT JOIN project_delivery d ON d.tenant_id=n.tenant_id AND d.project_node_id=n.id WHERE n.tenant_id=$1 AND n.deleted_at IS NULL`
		// Permission-scoped visibility is established by RequireTx on each bounded
		// inventory id; denied ids never contribute to private aggregates.
		rows, e := tx.Query(ctx, `SELECT n.id::text,coalesce(j.state,'pending'),d.project_node_id IS NOT NULL,j.project_node_id IS NOT NULL `+inventory+` ORDER BY n.id LIMIT 10001`, p.TenantID)
		if e != nil {
			return e
		}
		type entry struct {
			id, state           string
			adopted, discovered bool
		}
		entries := []entry{}
		for rows.Next() {
			var v entry
			if e = rows.Scan(&v.id, &v.state, &v.adopted, &v.discovered); e != nil {
				rows.Close()
				return e
			}
			entries = append(entries, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		out.CountsComplete = len(entries) <= 10000
		out.DiscoveryComplete = out.CountsComplete
		attention := false
		allAdopted := true
		ids := []string{}
		for i, v := range entries {
			if i == 10000 {
				break
			}
			if e = authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: v.id}); e != nil {
				if errors.Is(e, authz.ErrForbidden) {
					continue
				}
				return e
			}
			out.Counts["discovered"] += boolInt(v.discovered)
			state := v.state
			if v.adopted {
				state = "adopted"
			}
			out.Counts[state]++
			if !v.discovered {
				out.DiscoveryComplete = false
			}
			if !v.adopted {
				allAdopted = false
			}
			if state == "refused" || state == "retry_wait" {
				attention = true
			}
			if v.id > c.ID && (opt.State == "" || state == opt.State) && len(ids) < limit+1 {
				ids = append(ids, v.id)
			}
		}
		if len(ids) > limit {
			ids = ids[:limit]
			out.NextCursor = encodeCursor(pageCursor{Scope: scope, ID: ids[limit-1]})
		}
		for _, id := range ids {
			v, e := statusTx(ctx, tx, p, id)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		if attention {
			out.Status = "needs_attention"
		} else if allAdopted && out.CountsComplete && out.DiscoveryComplete {
			out.Status = "complete"
		}
		return nil
	})
	return out, err
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (s *Store) AdoptionReport(ctx context.Context, p tenant.Principal, project, cursor string, limit int, reporter AdoptionReporting) (AdoptionReport, error) {
	out := AdoptionReport{}
	limit, err := pageLimit(limit, 200)
	if err != nil || len(cursor) > 2048 {
		return out, errors.New("invalid report page")
	}
	if reporter == nil {
		return out, ErrAdoptionUnavailable
	}
	err = s.read(ctx, p, project, func(ctx context.Context, tx pgx.Tx) error { return nil })
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 60*time.Second)
	defer cancel()
	out, err = reporter.ReadReport(ctx, p, project, cursor, limit)
	if err != nil {
		return AdoptionReport{}, err
	}
	if len(out.Items) > limit || len(out.NextCursor) > 2048 {
		return AdoptionReport{}, errors.New("adoption report exceeds page bounds")
	}
	size := 0
	for _, raw := range out.Items {
		size += len(raw)
		if size > MaxSnapshotBytes {
			return AdoptionReport{}, errors.New("adoption report exceeds byte bound")
		}
	}
	return out, nil
}

var ErrAdoptionUnavailable = errors.New("automatic adoption reporting controller is unavailable")

func (s *Store) RequestAdoption(ctx context.Context, p tenant.Principal, project, action string, revision int64, reporter AdoptionReporting) (AdoptionJob, error) {
	var out AdoptionJob
	if reporter == nil {
		return out, ErrAdoptionUnavailable
	}
	if p.Kind != tenant.Person {
		return out, authz.ErrForbidden
	}
	if !uuid(project) || revision < 0 || (action != "preview" && action != "retry") {
		return out, errors.New("invalid adoption request")
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 60*time.Second)
	defer cancel()
	err := db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if e := transactionLimits(ctx, tx); e != nil {
			return e
		}
		if e := authz.LockProjectMutation(ctx, tx, p.TenantID); e != nil {
			return e
		}
		for _, permission := range []string{"releases.deploy", "journey.manage"} {
			if e := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); e != nil {
				return e
			}
		}
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL)`, p.TenantID, project).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return ErrNotFound
		}
		var err error
		out, err = reporter.RequestTx(ctx, tx, p, project, action, revision)
		return err
	})
	return out, err
}

// AdoptionVerification is P3's read-only verifier, kept separate from job writes.
type AdoptionVerification interface {
	Verify(context.Context, tenant.Principal, string) (json.RawMessage, error)
}

func (s *Store) VerifyAdoption(ctx context.Context, p tenant.Principal, project string, reporter AdoptionReporting) (json.RawMessage, error) {
	verifier, ok := reporter.(AdoptionVerification)
	if !ok {
		return nil, ErrAdoptionUnavailable
	}
	if err := s.read(ctx, p, project, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 60*time.Second)
	defer cancel()
	raw, err := verifier.Verify(ctx, p, project)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxSnapshotBytes || !json.Valid(raw) {
		return nil, errors.New("invalid verification evidence")
	}
	return raw, nil
}
