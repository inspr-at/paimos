// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var _ delivery.AdoptionReporting = (*Service)(nil)

type reportCursor struct {
	Tenant, Project, Digest string
	Offset                  int
}

// ReadReport implements P4a's persisted report boundary. Cursors bind tenant,
// project and digest; a replaced report fails CAS instead of mixing pages.
func (s *Service) ReadReport(ctx context.Context, p tenant.Principal, project, cursor string, limit int) (delivery.AdoptionReport, error) {
	out := delivery.AdoptionReport{Items: []json.RawMessage{}}
	c := reportCursor{Tenant: p.TenantID, Project: project}
	if len(cursor) > 2048 {
		return out, errors.New("invalid report cursor")
	}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || strictJSON(raw, &c) != nil || c.Tenant != p.TenantID || c.Project != project || !digestRE.MatchString(c.Digest) {
			return out, errors.New("invalid report cursor")
		}
	}
	page, err := s.Report(ctx, p, project, c.Digest, c.Offset, limit)
	if err != nil {
		return out, err
	}
	out.Revision = page.Status.Revision
	out.Incomplete = page.Status.Incomplete
	out.SourceFingerprint = page.Fingerprint
	add := func(kind string, value any) error {
		raw, err := json.Marshal(struct {
			Kind  string `json:"kind"`
			Value any    `json:"value"`
		}{kind, value})
		if err == nil {
			out.Items = append(out.Items, raw)
		}
		return err
	}
	for _, v := range page.Releases {
		if err = add("release", v); err != nil {
			return out, err
		}
	}
	for _, v := range page.Members {
		if err = add("member", v); err != nil {
			return out, err
		}
	}
	for _, v := range page.Reasons {
		if err = add("refusal", v); err != nil {
			return out, err
		}
	}
	if page.Next > 0 {
		c.Digest = page.Status.ReportDigest
		c.Offset = page.Next
		raw, _ := json.Marshal(c)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}

// RequestTx is deliberately storage/network free. P4a has already taken the
// canonical tree/access fences; do not acquire pairing after that prefix.
// The standalone Request method takes its full prefix before calling this.
func (s *Service) RequestTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, action string, expected int64) (delivery.AdoptionJob, error) {
	out := delivery.AdoptionJob{}
	if !uuidRE.MatchString(project) || (action != "preview" && action != "retry") || expected < 0 {
		return out, errors.New("invalid adoption request")
	}
	a, ok := s.authority(p.TenantID)
	if !ok {
		return out, ErrPrerequisite
	}
	if err := authz.LockProjectMutation(ctx, tx, p.TenantID); err != nil {
		return out, err
	}
	var person bool
	if err := tx.QueryRow(ctx, `SELECT kind='person' AND status='active' FROM principals WHERE id=$1`, p.ID).Scan(&person); err != nil {
		return out, err
	}
	if !person || p.Kind != tenant.Person {
		return out, authz.ErrForbidden
	}
	for _, perm := range []string{"releases.deploy", "journey.manage"} {
		if err := authz.RequireTx(ctx, tx, p, perm, authz.Scope{ProjectID: project}); err != nil {
			return out, err
		}
	}
	var revision int64
	var adopted, active bool
	err := tx.QueryRow(ctx, `SELECT revision,state='adopted',coalesce(lease_until>$2,false) FROM delivery_adoption_jobs WHERE project_node_id=$1 AND instance_id=$3 FOR NO KEY UPDATE`, project, s.now(), s.cfg.Instance).Scan(&revision, &adopted, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		if expected != 0 {
			return out, delivery.ErrRevisionChanged
		}
		if err = s.discoverProject(ctx, tx, a, project); err != nil {
			return out, err
		}
	} else {
		if err != nil {
			return out, err
		}
		if revision != expected {
			return out, delivery.ErrRevisionChanged
		}
		if active && !adopted {
			return out, ErrLease
		}
		if !adopted {
			if _, err = tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET state='pending',revision=revision+1,next_attempt_at=$2,reason_code=NULL,reason_message='',updated_at=$2 WHERE project_node_id=$1`, project, s.now()); err != nil {
				return out, err
			}
		}
	}
	err = tx.QueryRow(ctx, `SELECT state,revision,attempts,coalesce(reason_code,''),reason_message,last_checked_at,next_attempt_at,lease_until,coalesce(report_ref,''),coalesce(report_digest,''),coalesce(source_fingerprint,''),report_counts,report_incomplete,CASE WHEN evidence_attempt_id=attempt_id THEN coalesce(backup_ref,'') ELSE '' END,CASE WHEN evidence_attempt_id=attempt_id THEN backup_verified_at END,cleanup_state,next_reconcile_at,reserved_backup_bytes,reserved_restore_slots FROM delivery_adoption_jobs WHERE project_node_id=$1 AND instance_id=$2`, project, s.cfg.Instance).Scan(&out.State, &out.Revision, &out.Attempts, &out.ReasonCode, &out.ReasonMessage, &out.LastCheckedAt, &out.NextAttemptAt, &out.LeaseUntil, &out.ReportRef, &out.ReportDigest, &out.SourceFingerprint, &out.ReportCounts, &out.ReportIncomplete, &out.BackupRef, &out.BackupVerifiedAt, &out.CleanupState, &out.NextReconcileAt, &out.ReservedBackupBytes, &out.ReservedRestoreSlots)
	return out, err
}

func (s *Service) VerifyReport(ctx context.Context, p tenant.Principal, project string) (json.RawMessage, error) {
	v, err := s.Verify(ctx, p, project)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
