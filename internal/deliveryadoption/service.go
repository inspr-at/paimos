// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool     *pgxpool.Pool
	cfg      Config
	provider Provider
	reports  Reports
	now      func() time.Time
	wait     func(context.Context) error
}

func New(pool *pgxpool.Pool, cfg Config, provider Provider, reports Reports) (*Service, error) {
	if pool == nil || cfg.Instance == "" || len(cfg.Instance) > 128 || cfg.Artifact == "" || len(cfg.Artifact) > 512 || len(cfg.Authorities) > 10000 {
		return nil, ErrPrerequisite
	}
	seen := map[string]bool{}
	for _, a := range cfg.Authorities {
		if !uuidRE.MatchString(a.Executor.TenantID) || !uuidRE.MatchString(a.Executor.ID) || !uuidRE.MatchString(a.Authorizer.ID) || a.Executor.TenantID != a.Authorizer.TenantID || a.Authorizer.Kind != tenant.Person || a.Reference == "" || len(a.Reference) > 512 || seen[a.Executor.TenantID] {
			return nil, ErrPrerequisite
		}
		seen[a.Executor.TenantID] = true
	}
	cfg.Authorities = append([]Authority(nil), cfg.Authorities...)
	cfg.Product.HistorySequences = append([]int(nil), cfg.Product.HistorySequences...)
	return &Service{pool: pool, cfg: cfg, provider: provider, reports: reports, now: time.Now, wait: pollWait}, nil
}

func pollWait(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) WithClock(now func() time.Time) *Service { copy := *s; copy.now = now; return &copy }

func (s *Service) authority(tenantID string) (Authority, bool) {
	for _, a := range s.cfg.Authorities {
		if a.Executor.TenantID == tenantID {
			return a, true
		}
	}
	return Authority{}, false
}
func (s *Service) identity(tenantID, project, attempt string) Identity {
	return Identity{Instance: s.cfg.Instance, Tenant: tenantID, Project: project, Migration: Migration, Attempt: attempt}
}

// with performs bounded short checkpoints using the current principal's RLS.
// It never overrides request visibility or fabricates a migration principal.
func (s *Service) with(ctx context.Context, p tenant.Principal, fn func(pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 60*time.Second)
	defer cancel()
	return db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := limits(ctx, tx, "15s"); err != nil {
			return err
		}
		return fn(tx)
	})
}
func limits(ctx context.Context, tx pgx.Tx, statement string) error {
	d, ok := ctx.Deadline()
	if !ok {
		return errors.New("adoption operation requires deadline")
	}
	remaining := time.Until(d).Milliseconds()
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	_, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','5s',true),set_config('statement_timeout',$1,true),set_config('transaction_timeout',$2,true),set_config('idle_in_transaction_session_timeout',$2,true)`, statement, fmt.Sprintf("%dms", remaining))
	return err
}

// mutationFence is also used by discovery/retry/checkpoints. Importer key is
// first for apply only; canonical pairing -> tree -> tenant precedes job rows.
func mutationFence(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return err
	}
	return authz.LockProjectMutation(ctx, tx, p.TenantID)
}
func authorize(ctx context.Context, tx pgx.Tx, a Authority, project string) error {
	for _, p := range []tenant.Principal{a.Authorizer, a.Executor} {
		var kind, status string
		if err := tx.QueryRow(ctx, `SELECT kind,status FROM principals WHERE id=$1 AND tenant_id=$2`, p.ID, p.TenantID).Scan(&kind, &status); err != nil {
			return err
		}
		if status != "active" || kind != string(p.Kind) {
			return authz.ErrForbidden
		}
		for _, perm := range []string{"releases.deploy", "journey.manage"} {
			if err := authz.RequireTx(ctx, tx, p, perm, authz.Scope{ProjectID: project}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) snapshot(ctx context.Context, p tenant.Principal, fn func(pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = enterSnapshotPrincipal(ctx, tx, p); err != nil {
		return err
	}
	if err = limits(ctx, tx, "15s"); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func enterSnapshotPrincipal(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	var creator any
	if p.KeyCreatorID != "" {
		creator = p.KeyCreatorID
	}
	_, err := tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid)`, p.TenantID, p.ID, creator)
	return err
}

// DryRun is read-only; callers cannot supply mappings or recovery references.
func (s *Service) DryRun(ctx context.Context, p tenant.Principal, project string) (Report, error) {
	var r Report
	if !uuidRE.MatchString(project) {
		return r, delivery.ErrNotFound
	}
	err := s.snapshot(ctx, p, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var err error
		r, err = s.plan(ctx, tx, s.identity(p.TenantID, project, ""))
		return err
	})
	return r, err
}

const statusColumns = `j.project_node_id::text,CASE WHEN d.project_node_id IS NULL THEN 'journey' ELSE 'releases' END,j.state,j.revision,j.attempts,coalesce(j.reason_code,''),j.reason_message,coalesce(j.report_ref,''),coalesce(j.report_digest,''),j.report_incomplete,j.report_counts,j.last_checked_at,CASE WHEN j.evidence_attempt_id=j.attempt_id THEN j.backup_verified_at END,CASE WHEN j.evidence_attempt_id=j.attempt_id THEN coalesce(j.backup_ref,'') ELSE '' END,j.cleanup_state,j.next_reconcile_at,j.next_attempt_at`

func scanStatus(row pgx.Row) (Status, error) {
	var v Status
	var raw []byte
	err := row.Scan(&v.Project, &v.Mode, &v.State, &v.Revision, &v.Attempts, &v.ReasonCode, &v.Reason, &v.ReportRef, &v.ReportDigest, &v.Incomplete, &raw, &v.LastCheck, &v.BackupVerifiedAt, &v.BackupRef, &v.Cleanup, &v.NextCheck, &v.NextAttempt)
	if err == nil {
		err = json.Unmarshal(raw, &v.Counts)
	}
	return v, err
}
func (s *Service) Status(ctx context.Context, p tenant.Principal, project string) (Status, error) {
	var v Status
	if !uuidRE.MatchString(project) {
		return v, delivery.ErrNotFound
	}
	err := s.with(ctx, p, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var err error
		v, err = scanStatus(tx.QueryRow(ctx, `SELECT `+statusColumns+` FROM delivery_adoption_jobs j LEFT JOIN project_delivery d USING(tenant_id,project_node_id) WHERE j.project_node_id=$1 AND j.instance_id=$2`, project, s.cfg.Instance))
		if errors.Is(err, pgx.ErrNoRows) {
			var mode bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_delivery WHERE project_node_id=n.id) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1 AND k.slug='project'`, project).Scan(&mode); err != nil {
				return err
			}
			v = Status{Project: project, Mode: "journey", State: "pending", Incomplete: true, Cleanup: "none", ReasonCode: "rollout_dependency", Reason: "Local rollout authority and verified recovery evidence are required before automatic adoption."}
			if mode {
				v.Mode = "releases"
				v.State = "adopted"
				v.Incomplete = false
				v.ReasonCode = ""
				v.Reason = ""
			}
		}
		return err
	})
	return v, err
}

type ReportPage struct {
	Status      Status           `json:"status"`
	Fingerprint string           `json:"source_fingerprint"`
	Releases    []ReleaseMapping `json:"releases"`
	Members     []MemberMapping  `json:"members"`
	Reasons     []Reason         `json:"reasons"`
	Next        int              `json:"next_cursor,omitempty"`
}

// ReportPage indexes immutable persisted metadata, not live nodes. The cursor
// must be accompanied by the displayed report digest to reject stale pages.
func (s *Service) Report(ctx context.Context, p tenant.Principal, project, digest string, cursor, limit int) (ReportPage, error) {
	var out ReportPage
	if limit < 1 || limit > 200 || cursor < 0 || cursor > maxReportItems {
		return out, errors.New("invalid report page")
	}
	v, err := s.Status(ctx, p, project)
	if err != nil {
		return out, err
	}
	out.Status = v
	if s.reports == nil || v.ReportRef == "" {
		return out, ErrPrerequisite
	}
	if digest != "" && digest != v.ReportDigest {
		return out, delivery.ErrRevisionChanged
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := s.reports.Get(ctx, v.ReportRef)
	if err != nil {
		return out, err
	}
	if len(raw) > MaxReportBytes || sum(raw) != v.ReportDigest {
		return out, errors.New("report integrity verification failed")
	}
	var r Report
	if err = json.Unmarshal(raw, &r); err != nil {
		return out, err
	}
	if r.Identity.Instance != s.cfg.Instance || r.Identity.Tenant != p.TenantID || r.Identity.Project != project || r.Identity.Migration != Migration {
		return out, errors.New("report identity mismatch")
	}
	out.Fingerprint = r.Fingerprint
	total := len(r.Releases) + len(r.Members) + len(r.Reasons)
	if len(r.Releases) > 200 || len(r.Members) > 5000 || len(r.Reasons) > 5200 || total > maxReportItems || cursor > total {
		return out, errors.New("invalid report boundary")
	}
	end := min(total, cursor+limit)
	for i := cursor; i < end; i++ {
		if i < len(r.Releases) {
			out.Releases = append(out.Releases, r.Releases[i])
		} else if i < len(r.Releases)+len(r.Members) {
			out.Members = append(out.Members, r.Members[i-len(r.Releases)])
		} else {
			out.Reasons = append(out.Reasons, r.Reasons[i-len(r.Releases)-len(r.Members)])
		}
	}
	if end < total {
		out.Next = end
	}
	return out, ctx.Err()
}

// Request queues only preview/retry of the same automatic job, with job CAS.
// Final authority is checked against actual principal kind and current grants.
func (s *Service) Request(ctx context.Context, p tenant.Principal, project, action string, expected int64) (Status, error) {
	err := s.with(ctx, p, func(tx pgx.Tx) error {
		if err := mutationFence(ctx, tx, p); err != nil {
			return err
		}
		_, err := s.RequestTx(ctx, tx, p, project, action, expected)
		return err
	})
	if err != nil {
		return Status{}, err
	}
	return s.Status(ctx, p, project)
}

func (s *Service) discoverProject(ctx context.Context, tx pgx.Tx, a Authority, project string) error {
	if err := authz.RequireTx(ctx, tx, a.Executor, "releases.read", authz.Scope{ProjectID: project}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO delivery_adoption_jobs(tenant_id,project_node_id,instance_id,rollout_artifact_ref,executing_principal_id,authorizing_principal_id,rollout_authorization_ref,next_attempt_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(tenant_id,project_node_id) DO NOTHING`, a.Executor.TenantID, project, s.cfg.Instance, s.cfg.Artifact, a.Executor.ID, a.Authorizer.ID, a.Reference, s.now())
	return err
}

// Discover repairs one bounded tenant/project page per pass. Missing-job
// queries are themselves durable checkpoints: restart needs no volatile cursor.
// Deleted/inactive/imported projects are included rather than silently skipped.
func (s *Service) Discover(ctx context.Context, tenantCursor string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if tenantCursor == "" {
		tenantCursor = "00000000-0000-0000-0000-000000000000"
	}
	if !uuidRE.MatchString(tenantCursor) {
		return "", 0, errors.New("invalid tenant cursor")
	}
	var tenantID string
	err := s.pool.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id>=$1::uuid ORDER BY id LIMIT 1`, tenantCursor).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	a, ok := s.authority(tenantID)
	if !ok {
		return tenantID, 0, ErrPrerequisite
	}
	projects := []string{}
	err = s.with(ctx, a.Executor, func(tx pgx.Tx) error {
		if err := mutationFence(ctx, tx, a.Executor); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='project' AND NOT EXISTS(SELECT 1 FROM delivery_adoption_jobs j WHERE j.tenant_id=n.tenant_id AND j.project_node_id=n.id) ORDER BY n.id LIMIT 100`)
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
		if err != nil {
			return err
		}
		for _, id := range projects {
			if err = s.discoverProject(ctx, tx, a, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return tenantID, 0, err
	}
	if len(projects) == 100 {
		return tenantID, len(projects), nil
	}
	var next string
	err = s.pool.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id>$1::uuid ORDER BY id LIMIT 1`, tenantID).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return next, len(projects), err
}
