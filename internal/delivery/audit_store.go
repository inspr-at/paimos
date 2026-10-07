// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type MergeAudit struct {
	Repository   string     `json:"repository"`
	SHA          string     `json:"merge_sha"`
	PR           *int64     `json:"pull_request"`
	Ticket       *string    `json:"ticket_node_id"`
	Project      *string    `json:"project_id"`
	MergedBy     string     `json:"merged_by"`
	ViaQueue     bool       `json:"via_queue"`
	ChecksPassed bool       `json:"checks_passed"`
	ReviewOK     bool       `json:"review_ok"`
	Flags        []string   `json:"flags"`
	At           time.Time  `json:"at"`
	AlertedAt    *time.Time `json:"alerted_at"`
}

const auditColumns = `repository,merge_sha,pull_request,ticket_node_id::text,project_id::text,merged_by,via_queue,checks_passed,review_ok,flags,at,alerted_at`

func scanAudit(row pgx.Row) (MergeAudit, error) {
	var a MergeAudit
	err := row.Scan(&a.Repository, &a.SHA, &a.PR, &a.Ticket, &a.Project, &a.MergedBy, &a.ViaQueue, &a.ChecksPassed, &a.ReviewOK, &a.Flags, &a.At, &a.AlertedAt)
	return a, err
}

func (m *Module) auditExists(ctx context.Context, tid, sha string) (bool, error) {
	var present bool
	err := db.InTenant(db.AllProjects(ctx, "merge audit deduplication"), m.pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_merge_audit WHERE repository=$1 AND merge_sha=$2)`, m.config.Repository, sha).Scan(&present)
	})
	return present, err
}

// Ensure may create its principal.created event. Complete that preparation in
// a separate transaction, before the audit transaction acquires resource locks.
func (m *Module) auditActor(ctx context.Context, tid string) (tenant.Principal, error) {
	var p tenant.Principal
	err := db.InTenant(db.AllProjects(ctx, "merge audit System actor"), m.pool, tid, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, tid); err != nil {
			return err
		}
		var err error
		p, err = systemactor.Ensure(ctx, tx, tid)
		return err
	})
	return p, err
}

func auditLink(ctx context.Context, tx pgx.Tx, repo string, f MergeFact) (Observation, error) {
	o := Observation{Repository: repo, PR: f.PR, Branch: f.Branch, Head: f.Head}
	if f.PR != nil {
		err := tx.QueryRow(ctx, `SELECT ticket_node_id::text,project_id::text FROM delivery_items WHERE repository=$1 AND pull_request=$2`, repo, f.PR).Scan(&o.Ticket, &o.Project)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return o, err
		}
		if o.Ticket != nil {
			return o, nil
		}
	}
	return o, linkTx(ctx, tx, &o, f.Title)
}

func auditReview(ctx context.Context, tx pgx.Tx, o Observation) (bool, error) {
	if o.Ticket == nil || o.PR == nil {
		return false, nil
	}
	var order, head string
	var pr *int64
	err := tx.QueryRow(ctx, `SELECT work_order_id::text,head_sha,pull_request FROM work_order_reviews WHERE ticket_node_id=$1 AND repository=$2 ORDER BY created_at DESC,work_order_id DESC LIMIT 1`, o.Ticket, o.Repository).Scan(&order, &head, &pr)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Review binds the exact source head introduced by this PR merge, not the
	// distinct synthetic queue/squash commit and never a later ticket head.
	if head != o.Head || pr != nil && *pr != *o.PR {
		return false, nil
	}
	return reviewgate.VerifiedLatestTx(ctx, tx, order, *o.Ticket)
}

func auditChecksTx(ctx context.Context, tx pgx.Tx, repo, sha string, s Settings) ([]Check, error) {
	if s.RequiredChecks == nil {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (c->>'name') c->>'name',c->>'status',c->>'conclusion'
        FROM (
            SELECT e.sequence,e.head_sha,e.audit_checks AS checks FROM delivery_github_events e WHERE e.repository=$1 AND e.head_sha=$2 AND e.audit_checks IS NOT NULL
            UNION ALL
            SELECT e.sequence,o->>'head_sha',o->'checks' FROM delivery_github_events e CROSS JOIN LATERAL jsonb_array_elements(e.observations) o WHERE e.repository=$1 AND o->>'head_sha'=$2
        ) facts CROSS JOIN LATERAL jsonb_array_elements(coalesce(facts.checks,'[]'::jsonb)) c
        WHERE c->>'name'=ANY($3::text[])
        ORDER BY c->>'name',facts.sequence DESC LIMIT 65`, repo, sha, *s.RequiredChecks)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Check{}
	for rows.Next() {
		var c Check
		if err = rows.Scan(&c.Name, &c.Status, &c.Conclusion); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func auditFlags(a MergeAudit) []string {
	flags := []string{}
	if !a.ViaQueue {
		flags = append(flags, "no_queue")
	}
	if !a.ChecksPassed {
		flags = append(flags, "checks_missing")
	}
	if !a.ReviewOK {
		flags = append(flags, "no_review")
	}
	if a.PR == nil {
		flags = append(flags, "direct_push")
	}
	return flags
}

type auditRecipient struct {
	principal string
	session   *string
}

// Tenant/tree fences precede node, lead, session and principal locks. Lead
// control and ACL changes cannot change the recipient after this final check.
func auditRecipientTx(ctx context.Context, tx pgx.Tx, tid string, a MergeAudit) (*auditRecipient, error) {
	if a.Project == nil {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM nodes WHERE id=$1 OR id=$2 ORDER BY id FOR KEY SHARE`, a.Project, a.Ticket)
	if err != nil {
		return nil, err
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	var owner string
	var session *string
	err = tx.QueryRow(ctx, `SELECT owner_principal_id::text,session_id::text FROM project_leads WHERE project_id=$1 FOR NO KEY UPDATE`, a.Project).Scan(&owner, &session)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	recipient := auditRecipient{principal: owner}
	kind := tenant.Person
	if session != nil {
		var id string
		err = tx.QueryRow(ctx, `SELECT agent_principal_id::text FROM harness_sessions WHERE id=$1 AND project_id=$2 AND role='coordinator' AND stopped_at IS NULL AND archived_at IS NULL FOR NO KEY UPDATE`, session, a.Project).Scan(&id)
		if err == nil {
			recipient = auditRecipient{principal: id, session: session}
			kind = tenant.Agent
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT status='active' AND kind=$2 FROM principals WHERE id=$1 FOR KEY SHARE`, recipient.principal, kind).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	// This checks recipient role access only; it grants no API key or action
	// authority. Internal notices still require current project read access.
	p := tenant.Principal{ID: recipient.principal, TenantID: tid, Kind: kind, Scopes: []string{"delivery.read", "nodes.read"}}
	for _, permission := range []string{"delivery.read", "nodes.read"} {
		if err = authz.RequireTx(ctx, tx, p, permission, scope(a.Project)); errors.Is(err, authz.ErrForbidden) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
	}
	return &recipient, nil
}

func auditAlert(ctx context.Context, tx pgx.Tx, actor tenant.Principal, a MergeAudit, to *auditRecipient) error {
	if to == nil {
		return nil
	}
	// Serialize using the tenant fence and audit PK, before the first append.
	key := digest([]byte(actor.TenantID + "|" + a.Repository + "|" + a.SHA))
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_messages WHERE sender_principal_id=$1 AND idempotency_key=$2)`, actor.ID, key).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	body := fmt.Sprintf("Merge audit: %s %s bypassed delivery safeguards (%s).", a.Repository, a.SHA, strings.Join(a.Flags, ", "))
	_, err := inbox.AcceptMessageTx(ctx, tx, actor, inbox.Acceptance{RecipientPrincipalID: to.principal, RecipientSessionID: to.session, SenderLabel: "Delivery", Body: body, IdempotencyKey: key, ProjectID: a.Project})
	return err
}

func (m *Module) auditMerge(ctx context.Context, tid string, f MergeFact, reader AuditGitHub) error {
	if tid != m.config.TenantID || reader == nil {
		return errNotConfigured
	}
	if !reviewgate.ValidSHA(f.SHA) || !reviewgate.ValidSHA(f.Head) || f.At.IsZero() || len(f.MergedBy) > 100 || len(f.Title) > 4096 || len(f.Branch) > 255 || f.PR != nil && *f.PR < 1 {
		return errRead
	}
	present, err := m.auditExists(ctx, tid, f.SHA)
	if err != nil || present {
		return err
	}
	// Resolve missing checks with the existing read-only installation token.
	// No transaction or row lock spans this external lookup.
	var cs []Check
	complete := false
	err = db.InTenant(db.AllProjects(ctx, "merge audit stored checks"), m.pool, tid, func(tx pgx.Tx) error {
		o, err := auditLink(ctx, tx, m.config.Repository, f)
		if err != nil {
			return err
		}
		s, err := settingsTx(ctx, tx, o.Project)
		if err != nil {
			return err
		}
		cs, err = auditChecksTx(ctx, tx, m.config.Repository, f.SHA, s)
		complete = allSuccess(Observation{Settings: s, Checks: cs})
		return err
	})
	if err != nil {
		return err
	}
	if !complete {
		cs, err = reader.AuditChecks(ctx, f.SHA)
		if err != nil {
			return err
		}
	}
	if len(cs) > 4000 {
		return errRead
	}
	actor, err := m.auditActor(ctx, tid)
	if err != nil {
		return err
	}
	return db.InTenant(db.AllProjects(ctx, "authenticated merge audit write"), m.pool, tid, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, tid); err != nil {
			return err
		}
		present := false
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_merge_audit WHERE repository=$1 AND merge_sha=$2)`, m.config.Repository, f.SHA).Scan(&present); err != nil || present {
			return err
		}
		o, err := auditLink(ctx, tx, m.config.Repository, f)
		if err != nil {
			return err
		}
		s, err := settingsTx(ctx, tx, o.Project)
		if err != nil {
			return err
		}
		a := MergeAudit{Repository: m.config.Repository, SHA: f.SHA, PR: f.PR, Ticket: o.Ticket, Project: o.Project, MergedBy: f.MergedBy, At: f.At}
		a.ViaQueue = f.MergedBy == "github-merge-queue[bot]"
		var group bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_github_events WHERE repository=$1 AND event='merge_group' AND action='checks_requested' AND head_sha=$2)`, a.Repository, a.SHA).Scan(&group); err != nil {
			return err
		}
		a.ViaQueue = a.ViaQueue || group
		if complete {
			// Re-read under the final fence, including settings changes made
			// during any earlier preparation or external work.
			cs, err = auditChecksTx(ctx, tx, a.Repository, a.SHA, s)
			if err != nil {
				return err
			}
		}
		a.ChecksPassed = allSuccess(Observation{Settings: s, Checks: cs})
		a.ReviewOK, err = auditReview(ctx, tx, o)
		if err != nil {
			return err
		}
		a.Flags = auditFlags(a)
		// Resolve/authorize all FK parents and the current lead before events.
		var to *auditRecipient
		if len(a.Flags) > 0 {
			to, err = auditRecipientTx(ctx, tx, tid, a)
			if err != nil {
				return err
			}
			if to != nil {
				at := m.now()
				a.AlertedAt = &at
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO delivery_merge_audit(tenant_id,repository,merge_sha,pull_request,ticket_node_id,project_id,merged_by,via_queue,checks_passed,review_ok,flags,at,alerted_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, tid, a.Repository, a.SHA, a.PR, a.Ticket, a.Project, a.MergedBy, a.ViaQueue, a.ChecksPassed, a.ReviewOK, a.Flags, a.At, a.AlertedAt)
		if err != nil {
			return err
		}
		if len(a.Flags) == 0 {
			return nil
		}
		if _, err = events.Append(ctx, tx, actor, events.Change{Type: "delivery.bypass_merge", NodeID: a.Ticket, After: a, At: &a.At}); err != nil {
			return err
		}
		return auditAlert(ctx, tx, actor, a, to)
	})
}
