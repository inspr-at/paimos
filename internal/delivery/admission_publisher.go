// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

// AdmissionGitHub is the sole admission write boundary. Implementations bind
// all calls to the host-configured repository; credentials never enter delivery.
// Status returns the latest aeon/admission context (including other writers).
// Enqueue must use expectedHeadOid, never auto-merge.
type AdmissionGitHub interface {
	Status(context.Context, string) (string, error)
	PostStatus(context.Context, string, string) error
	Enqueue(context.Context, int64, string) error
	Dequeue(context.Context, int64) error
}

func (m *Module) WithAdmission(g AdmissionGitHub) *Module { m.admissionGitHub = g; return m }

type admissionMember struct {
	PR       int64  `json:"pr"`
	Head     string `json:"head"`
	Revision int64  `json:"revision"`
	Project  string `json:"project"`
}
type admissionPublication struct {
	SHA, Kind                                         string
	Members                                           []admissionMember
	Revision                                          int64
	Invalidated, Active, Uncertain, Resolved, Retired bool
	PublishedRevision                                 int64
	ConfirmedState                                    string
}

// All writers hold the tenant fence. Group membership is immutable for this
// synthetic SHA; a duplicate cannot clear an invalidation or revive a group.
func stageAdmissionGroupTx(ctx context.Context, tx pgx.Tx, tid, repository, sha string, pulls []Pull, active, resolved bool) error {
	members := []admissionMember{}
	seen := map[int64]bool{}
	if len(pulls) > 100 {
		return errRead
	}
	for _, p := range pulls {
		if p.Number < 1 || seen[p.Number] || !reviewgate.ValidSHA(p.Head) {
			return errRead
		}
		seen[p.Number] = true
		member := admissionMember{PR: p.Number, Head: p.Head}
		err := tx.QueryRow(ctx, `SELECT revision,coalesce(project_id::text,'') FROM delivery_admission_bindings WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, repository, p.Number, p.Head).Scan(&member.Revision, &member.Project)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		members = append(members, member)
	}
	raw, _ := json.Marshal(members)
	_, err := tx.Exec(ctx, `INSERT INTO delivery_admission_outbox(tenant_id,repository,sha,context,kind,members,active,invalidated,resolved,retired)
 VALUES($1,$2,$3,$4,'group',$5,$6,$7,$8,NOT $6)
 ON CONFLICT(tenant_id,repository,sha,context) DO UPDATE SET
 members=CASE WHEN NOT delivery_admission_outbox.resolved AND EXCLUDED.resolved THEN EXCLUDED.members ELSE delivery_admission_outbox.members END,
 resolved=delivery_admission_outbox.resolved OR EXCLUDED.resolved,
 active=delivery_admission_outbox.active AND EXCLUDED.active,
 invalidated=delivery_admission_outbox.invalidated OR NOT EXCLUDED.active,
 retired=delivery_admission_outbox.retired OR NOT EXCLUDED.active,
 revision=delivery_admission_outbox.revision+1`, tid, repository, sha, AdmissionContext, raw, active, !active, resolved && len(pulls) > 0)
	return err
}

func (m *Module) admissionStateTx(ctx context.Context, tx pgx.Tx, sha string) (admissionPublication, string, error) {
	p := admissionPublication{SHA: sha}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT kind,members,revision,invalidated,active,uncertain,resolved,published_revision,confirmed_state,retired IS TRUE FROM delivery_admission_outbox WHERE repository=$1 AND sha=$2 AND context=$3`, m.config.Repository, sha, AdmissionContext).Scan(&p.Kind, &raw, &p.Revision, &p.Invalidated, &p.Active, &p.Uncertain, &p.Resolved, &p.PublishedRevision, &p.ConfirmedState, &p.Retired)
	if err != nil {
		return p, "", err
	}
	if p.Retired {
		return p, p.ConfirmedState, nil
	}
	if len(raw) > 64<<10 {
		return p, "failure", nil
	}
	if err = json.Unmarshal(raw, &p.Members); err != nil {
		return p, "", err
	}
	if p.Invalidated || !p.Active || !p.Resolved || len(p.Members) == 0 || len(p.Members) > 100 {
		return p, "failure", nil
	}
	for _, member := range p.Members {
		item, err := load(ctx, tx, stableID(m.config.TenantID, m.config.Repository, subject(&member.PR, nil)))
		if err != nil {
			return p, "", err
		}
		if item == nil || item.Head != member.Head {
			return p, "failure", nil
		}
		snap, err := AdmissionTx(ctx, tx, *item)
		var problem *apiError
		if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &problem) {
			return p, "failure", nil
		}
		if err != nil {
			return p, "", err
		}
		if !snap.Decision.Admissible || snap.BindingRevision != member.Revision {
			return p, "failure", nil
		}
		var quarantined bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_admission_queue WHERE repository=$1 AND pull_request=$2 AND head_sha=$3 AND quarantined)
 OR EXISTS(SELECT 1 FROM delivery_queue_failures WHERE repository=$1 AND pull_request=$2 AND head_sha=$3 AND kind='required_failure')`, m.config.Repository, member.PR, member.Head).Scan(&quarantined)
		if err != nil {
			return p, "", err
		}
		if quarantined || item.Observation.QueueFailure {
			return p, "failure", nil
		}
	}
	return p, "success", nil
}

// publishAdmission owns one cross-process session lock per repo/SHA/context.
// It holds no tenant/row transaction during HTTP, so a hold can commit while a
// publisher stalls. Every dispatch re-reads current authority, never a queued
// payload. Revision CAS prevents acknowledging a state invalidated mid-POST.
func (m *Module) publishAdmission(ctx context.Context, sha string) error {
	conn, release, err := db.Acquire(ctx, m.pool)
	if err != nil {
		return err
	}
	defer release()
	key := "aeon-admission:" + m.config.Repository + ":" + sha + ":" + AdmissionContext
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil || !locked {
		return err
	}
	defer unlock(conn, key)
	ctx = db.AllProjects(db.WithConnection(ctx, m.pool, conn), "admission serialized publisher")
	// Bounded reconciliation leaves the durable row pending on persistent GitHub
	// failures. Restart and the periodic sweep retry it, including uncertain posts.
	for attempt := 0; attempt < 8; attempt++ {
		var p admissionPublication
		var state string
		err = db.InTenant(ctx, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
				return err
			}
			var err error
			p, state, err = m.admissionStateTx(ctx, tx, sha)
			if err != nil || p.Retired {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE delivery_admission_outbox SET uncertain=true WHERE repository=$1 AND sha=$2 AND context=$3`, m.config.Repository, sha, AdmissionContext)
			return err
		})
		if err != nil {
			return err
		}
		// A merged head or destroyed group keeps its last confirmed state.
		if p.Retired {
			return nil
		}
		// Reconcile confirmed revisions with a read. Avoid consuming GitHub's
		// per-SHA/context status limit with identical periodic POSTs. An
		// uncertain dispatch always reads and reposts, even if the state agrees.
		observed, readErr := m.admissionGitHub.Status(ctx, sha)
		var postErr error
		if p.Uncertain || p.PublishedRevision != p.Revision || p.ConfirmedState != state || readErr != nil || observed != state {
			postErr = m.admissionGitHub.PostStatus(ctx, sha, state)
			observed, readErr = m.admissionGitHub.Status(ctx, sha)
		}
		if postErr != nil || readErr != nil || observed != state {
			continue
		}
		acknowledged := false
		err = db.InTenant(ctx, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
				return err
			}
			current, want, err := m.admissionStateTx(ctx, tx, sha)
			if err != nil {
				return err
			}
			if current.Revision != p.Revision || want != state {
				return nil
			}
			tag, err := tx.Exec(ctx, `UPDATE delivery_admission_outbox SET published_revision=$4,confirmed_state=$5,uncertain=false,checked_at=$6 WHERE repository=$1 AND sha=$2 AND context=$3 AND revision=$4`, m.config.Repository, sha, AdmissionContext, p.Revision, state, m.now())
			acknowledged = tag.RowsAffected() == 1
			return err
		})
		if err != nil {
			return err
		}
		if acknowledged {
			return nil
		}
	}
	return fmt.Errorf("admission publication remains unconfirmed")
}

// RunAdmission is opt-in host configuration, independent of P4's ruleset cutover.
// A new process resumes all pending rows; confirmed rows are also reconciled to
// repair delayed HTTP outcomes or writes by an obsolete external publisher.
func (m *Module) RunAdmission(ctx context.Context) {
	if m.admissionGitHub == nil {
		return
	}
	// Queue removal is the enforcement lane. A stalled status publisher must
	// never delay it or spend the time budget needed to confirm a removal.
	done := make(chan struct{})
	go func() { defer close(done); m.runAdmissionLane(ctx, m.reconcileAdmissionQueue) }()
	m.runAdmissionLane(ctx, m.reconcileAdmission)
	<-done
}
func (m *Module) runAdmissionLane(ctx context.Context, run func(context.Context) error) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		op, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := run(op); err != nil && ctx.Err() == nil {
			slog.Warn("admission reconciliation incomplete")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (m *Module) reconcileAdmission(ctx context.Context) error {
	service := db.AllProjects(ctx, "admission outbox inventory")
	if err := m.seedAdmission(service); err != nil {
		return err
	}
	// Keyset pages bound memory. Historical, merged and destroyed SHAs stay retired.
	cursor := ""
	var failures []error
	for {
		shas := []string{}
		err := db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT sha FROM delivery_admission_outbox WHERE repository=$1 AND context=$2 AND sha>$3 AND retired IS NOT TRUE AND (published_revision<revision OR uncertain OR checked_at IS NULL OR checked_at<$4) AND ((kind='group' AND active) OR (kind='head' AND EXISTS(SELECT 1 FROM delivery_items i WHERE i.tenant_id=delivery_admission_outbox.tenant_id AND i.repository=delivery_admission_outbox.repository AND i.head_sha=delivery_admission_outbox.sha AND i.pull_request IS NOT NULL AND i.observation->>'open'='true'))) ORDER BY sha LIMIT 100`, m.config.Repository, AdmissionContext, cursor, m.now().Add(-30*time.Second))
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var sha string
				if err = rows.Scan(&sha); err != nil {
					return err
				}
				shas = append(shas, sha)
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		for _, sha := range shas {
			if err = m.publishAdmission(ctx, sha); err != nil {
				failures = append(failures, err)
			}
		}
		if len(shas) < 100 {
			break
		}
		cursor = shas[len(shas)-1]
	}
	return errors.Join(failures...)
}
