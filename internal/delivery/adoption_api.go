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

type DeliveryStatus struct {
	ProjectID      string        `json:"project_id"`
	ProductProject bool          `json:"product_project"`
	Title          string        `json:"title,omitempty"`
	Mode           string        `json:"mode"`
	Revision       int64         `json:"revision"`
	NextSequence   int           `json:"next_sequence,omitempty"`
	BuildDefaults  BuildSettings `json:"build_defaults"`
	Adoption       *AdoptionJob  `json:"adoption"`
}

const adoptionColumns = `j.state,j.revision,j.attempts,coalesce(j.reason_code,''),j.reason_message,j.last_checked_at,j.next_attempt_at,j.lease_until,coalesce(j.report_ref,''),coalesce(j.report_digest,''),coalesce(j.source_fingerprint,''),j.report_counts,j.report_incomplete,coalesce(j.backup_ref,''),j.backup_verified_at,j.cleanup_state,j.next_reconcile_at,j.reserved_backup_bytes,j.reserved_restore_slots`

func scanJob(row pgx.Row) (AdoptionJob, error) {
	var j AdoptionJob
	err := row.Scan(&j.State, &j.Revision, &j.Attempts, &j.ReasonCode, &j.ReasonMessage, &j.LastCheckedAt, &j.NextAttemptAt, &j.LeaseUntil, &j.ReportRef, &j.ReportDigest, &j.SourceFingerprint, &j.ReportCounts, &j.ReportIncomplete, &j.BackupRef, &j.BackupVerifiedAt, &j.CleanupState, &j.NextReconcileAt, &j.ReservedBackupBytes, &j.ReservedRestoreSlots)
	return j, err
}
func statusTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) (DeliveryStatus, error) {
	return inventoryStatusTx(ctx, tx, p, project, false)
}
func inventoryStatusTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, includeInactive bool) (DeliveryStatus, error) {
	out := DeliveryStatus{ProjectID: project, Mode: "journey"}
	var defaults []byte
	err := tx.QueryRow(ctx, `SELECT n.title,coalesce(d.revision,0),coalesce(d.next_sequence,0),coalesce(d.build_defaults,'{}'::jsonb),d.project_node_id IS NOT NULL FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id AND (k.slug='project' OR $3) LEFT JOIN project_delivery d ON d.tenant_id=n.tenant_id AND d.project_node_id=n.id WHERE n.tenant_id=$1 AND n.id=$2 AND (n.deleted_at IS NULL OR $3)`, p.TenantID, project, includeInactive).Scan(&out.Title, &out.Revision, &out.NextSequence, &defaults, new(bool))
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

// adoptionRead also admits refused deleted/converted project records for
// workspace operators. Ordinary release reads continue to require live projects.
func (s *Store) adoptionRead(ctx context.Context, p tenant.Principal, project string, fn func(context.Context, pgx.Tx) error) error {
	if !uuid(project) {
		return invalidInput("invalid project identity")
	}
	return s.read(ctx, p, "", func(ctx context.Context, tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var active bool
		err := tx.QueryRow(ctx, `SELECT n.deleted_at IS NULL AND k.slug='project' FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1 AND n.id=$2 AND (k.slug='project' OR EXISTS(SELECT 1 FROM delivery_adoption_jobs j WHERE j.tenant_id=n.tenant_id AND j.project_node_id=n.id))`, p.TenantID, project).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !active {
			if err := authz.RequireTx(ctx, tx, p, "roles.manage", authz.Scope{}); err != nil {
				if errors.Is(err, authz.ErrForbidden) {
					return ErrNotFound
				}
				return err
			}
		}
		return fn(ctx, tx)
	})
}
func (s *Store) Status(ctx context.Context, p tenant.Principal, project string) (DeliveryStatus, error) {
	var out DeliveryStatus
	err := s.adoptionRead(ctx, p, project, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = inventoryStatusTx(ctx, tx, p, project, true)
		out.ProductProject = s.productTenant == p.TenantID && s.productProject == project
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
		return out, invalidInput("invalid adoption state")
	}
	err = s.read(ctx, p, "", func(ctx context.Context, tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		// Check each project's actual releases.read grant before counts or
		// pagination; project visibility by itself is not release-read authority.
		operator := authz.RequireTx(ctx, tx, p, "roles.manage", authz.Scope{}) == nil
		canRead, e := authz.ReadPermissionCheckerTx(ctx, tx, p, "releases.read")
		if e != nil {
			return e
		}
		inventory := `FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id LEFT JOIN delivery_adoption_jobs j ON j.tenant_id=n.tenant_id AND j.project_node_id=n.id LEFT JOIN project_delivery d ON d.tenant_id=n.tenant_id AND d.project_node_id=n.id WHERE n.tenant_id=$1 AND (k.slug='project' OR ($2 AND j.project_node_id IS NOT NULL)) AND (n.deleted_at IS NULL OR $2)`
		// Permission-scoped visibility is established from current grants for each bounded
		// inventory id; denied ids never contribute to private aggregates.
		rows, e := tx.Query(ctx, `SELECT n.id::text,coalesce(j.state,'pending'),d.project_node_id IS NOT NULL,j.project_node_id IS NOT NULL `+inventory+` ORDER BY n.id LIMIT 10001`, p.TenantID, operator)
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
			if e = canRead(v.id); e != nil {
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

		}
		ids = []string{}
		rows, e = tx.Query(ctx, `SELECT n.id::text,coalesce(j.state,'pending'),d.project_node_id IS NOT NULL,j.project_node_id IS NOT NULL `+inventory+` AND n.id>$3::uuid AND ($4='' OR CASE WHEN d.project_node_id IS NOT NULL THEN 'adopted' ELSE coalesce(j.state,'pending') END=$4) ORDER BY n.id LIMIT 501`, p.TenantID, operator, c.ID, opt.State)
		if e != nil {
			return e
		}
		pageEntries := []entry{}
		for rows.Next() {
			var v entry
			if e = rows.Scan(&v.id, &v.state, &v.adopted, &v.discovered); e != nil {
				rows.Close()
				return e
			}
			pageEntries = append(pageEntries, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		lastScanned := ""
		for i, v := range pageEntries {
			if i == 500 {
				out.NextCursor = encodeCursor(pageCursor{Scope: scope, ID: lastScanned})
				break
			}
			lastScanned = v.id
			if e = canRead(v.id); e != nil {
				if errors.Is(e, authz.ErrForbidden) {
					continue
				}
				return e
			}
			if len(ids) == limit {
				out.NextCursor = encodeCursor(pageCursor{Scope: scope, ID: ids[len(ids)-1]})
				break
			}
			ids = append(ids, v.id)
		}

		for _, id := range ids {
			v, e := inventoryStatusTx(ctx, tx, p, id, operator)
			if e != nil {
				return e
			}
			v.ProductProject = s.productTenant == p.TenantID && s.productProject == id
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
		return out, invalidInput("invalid report page")
	}
	if reporter == nil {
		return out, ErrAdoptionUnavailable
	}
	err = s.adoptionRead(ctx, p, project, func(ctx context.Context, tx pgx.Tx) error { return nil })
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
	VerifyReport(context.Context, tenant.Principal, string) (json.RawMessage, error)
}

func (s *Store) VerifyAdoption(ctx context.Context, p tenant.Principal, project string, reporter AdoptionReporting) (json.RawMessage, error) {
	verifier, ok := reporter.(AdoptionVerification)
	if !ok {
		return nil, ErrAdoptionUnavailable
	}
	if err := s.adoptionRead(ctx, p, project, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 60*time.Second)
	defer cancel()
	raw, err := verifier.VerifyReport(ctx, p, project)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxSnapshotBytes || !json.Valid(raw) {
		return nil, errors.New("invalid verification evidence")
	}
	return raw, nil
}
