// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// WithPublication enables delivery only for this running tagged build. Replaying
// the same build is safe: receipts survive restarts and Undo. There is no scan of
// historic releases here; OPS reviews that inventory separately.
func (m *Module) WithPublication(p *releasehistory.Publication, current, tenantID, repository string) *Module {
	m.publication = nil
	m.publicationTenant = ""
	if p != nil && p.Version == current && p.Tag == "v"+current && p.Repository != "" && p.Repository == repository && tenantID != "" && p.ProjectKey != "" && p.Commit != "" && len(p.Commits) <= releasehistory.MaxPublicationCommits {
		m.publication = p
		m.publicationTenant = tenantID
	} else if p != nil {
		slog.Warn("release publication automation disabled: running version or configured GitHub App ownership does not match build evidence", "version", current)
	}
	return m
}

func (m *Module) gitDecision(n node, merge string) decision {
	p := m.publication
	sha, _ := json.Marshal(merge)
	n.Fields = map[string]json.RawMessage{"pr_url": n.Fields["pr_url"], "merge_commit": sha}
	source := fmt.Sprintf("release %s (%s, %s)", p.Name, p.Version, p.Tag)
	return publicationDecision(n, "git/"+p.Repository+"/"+p.Tag, "Shipped in "+source+".", source)
}

// The newest verified observation wins, including a follow-up after this tag.
// Editable merge_commit fields cannot override immutable audit evidence.
// On a tied timestamp, an outside-tag fact wins conservatively: SHA ordering
// cannot prove which merge came last.
func (m *Module) gitMemberTx(ctx context.Context, tx pgx.Tx, c candidate) (string, error) {
	p := m.publication
	n := c.Node
	var sha *string
	var key string
	var linked bool
	err := tx.QueryRow(ctx, `SELECT n.key, EXISTS(SELECT 1 FROM nodes project WHERE project.tenant_id=n.tenant_id AND project.id=n.project_id AND project.deleted_at IS NULL AND project.fields->>'project_key'=$3 AND current_setting('aeon.tenant_id')=$4),
 (SELECT f.sha FROM (
 SELECT e.metadata->>'merge_sha' sha,(e.metadata->>'merged_at')::timestamptz at FROM events e
 WHERE e.tenant_id=n.tenant_id AND e.node_id=n.id AND `+mergeEvidenceSQL+` AND e.metadata->>'repository'=$2
 UNION ALL
 SELECT a.merge_sha,a.at FROM delivery_merge_audit a WHERE a.tenant_id=n.tenant_id AND a.ticket_node_id=n.id AND a.project_id=n.project_id AND a.repository=$2 AND a.pull_request IS NOT NULL
 ) f ORDER BY f.at DESC,(f.sha=ANY($5::text[])),f.sha LIMIT 1) FROM nodes n WHERE n.id=$1`, n.ID, p.Repository, p.ProjectKey, m.publicationTenant, p.Commits).Scan(&key, &linked, &sha)
	if err != nil || !linked {
		return "", err
	}
	if sha == nil {
		if c.Since.After(p.TaggedAt) || !slices.Contains(p.Commits, p.Tickets[key]) {
			return "", nil
		}
		return p.Tickets[key], nil
	}
	if !slices.Contains(p.Commits, *sha) {
		return "", nil
	}
	return *sha, nil
}

// Select an existing responsible person, without provisioning any role or key.
// The final RequireTx runs under the tenant/access fence. Events still name the
// System actor because this is the workspace's unattended status policy.
func publicationAuthorityTx(ctx context.Context, tx pgx.Tx, tid, project string) error {
	rows, err := tx.Query(ctx, `SELECT p.id::text FROM principals p JOIN role_bindings b ON b.tenant_id=p.tenant_id AND b.principal_id=p.id
 JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
 WHERE p.kind='person' AND p.status='active' AND p.linked_to IS NULL
 AND (b.scope_type='workspace' OR b.scope_type='project' AND b.scope_id=$1::uuid) GROUP BY p.id ORDER BY min(CASE r.key WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'member' THEN 2 ELSE 3 END),p.id::text LIMIT 16`, project)
	if err != nil {
		return err
	}
	ids, err := collectIDs(rows)
	if err != nil {
		return err
	}
	for _, id := range ids {
		err = authz.RequireTx(ctx, tx, tenant.Principal{ID: id, TenantID: tid, Kind: tenant.Person}, "nodes.write", authz.Scope{ProjectID: project})
		if err == nil {
			return nil
		}
		if !errors.Is(err, authz.ErrForbidden) {
			return err
		}
	}
	return authz.ErrForbidden
}

func (m *Module) publishGit(ctx context.Context, actor tenant.Principal, now time.Time) error {
	if m.publication == nil || m.publicationTenant != actor.TenantID {
		return nil
	}
	after := ""
	for {
		count := 0
		err := db.InTenant(ctx, m.pool, actor.TenantID, func(tx pgx.Tx) error {
			if err := db.LockWorkTreeTx(ctx, tx); err != nil {
				return err
			}
			s, err := Load(ctx, tx)
			if err != nil || !s.Rules["publish"].Enabled || s.ModeAt(now) == "off" {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 JOIN nodes project ON project.tenant_id=n.tenant_id AND project.id=n.project_id AND project.deleted_at IS NULL AND project.fields->>'project_key'=$1
 WHERE n.deleted_at IS NULL AND k.slug='work' AND `+candidateStateSQL+`='done'
 AND ($2='' OR n.id>nullif($2,'')::uuid) ORDER BY n.id LIMIT $3`, m.publication.ProjectKey, after, batchSize)
			if err != nil {
				return err
			}
			ids, err := collectIDs(rows)
			if err != nil {
				return err
			}
			count = len(ids)
			if count == 0 {
				return nil
			}
			after = ids[count-1]
			candidates, err := loadCandidates(ctx, tx, ids, now)
			if err != nil {
				return err
			}
			// Lock actor and all ticket rows before the first event-counter acquisition.
			if _, err = tx.Exec(ctx, `SELECT id FROM principals WHERE id=$1 FOR KEY SHARE`, actor.ID); err != nil {
				return err
			}
			type change struct {
				n node
				d decision
			}
			changes := []change{}
			for _, c := range candidates {
				n := c.Node
				if n.ProjectID == nil || classic(n) || noReleaseNeeded(n) {
					continue
				}
				parent, err := db.WorkStatusParentTx(ctx, tx, n.ID)
				if err != nil {
					return err
				}
				var leaf bool
				if err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM nodes child JOIN node_kinds k ON k.tenant_id=child.tenant_id AND k.id=child.kind_id WHERE child.parent_id=$1 AND child.deleted_at IS NULL AND k.slug='work')`, n.ID).Scan(&leaf); err != nil {
					return err
				}
				if parent || !leaf {
					continue
				}
				o, err := Project(ctx, tx, *n.ProjectID, s.Enabled)
				if err != nil {
					return err
				}
				if !o.Effective || o.Mode == "off" {
					continue
				}
				member, err := m.gitMemberTx(ctx, tx, c)
				if err != nil {
					return err
				}
				if member == "" {
					continue
				}
				if err = publicationAuthorityTx(ctx, tx, actor.TenantID, *n.ProjectID); err != nil {
					return err
				}
				changes = append(changes, change{n, m.gitDecision(n, member)})
			}
			for _, c := range changes {
				if err = enact(ctx, tx, actor, c.n, c.d, s.ModeAt(now)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if count < batchSize {
			return nil
		}
	}
}

func (m *Module) gitProposalEligible(ctx context.Context, tx pgx.Tx, c candidate, d decision) error {
	if m.publication == nil || pending(c.Node) {
		return events.ErrConflict
	}
	member, err := m.gitMemberTx(ctx, tx, c)
	if err != nil {
		return err
	}
	if member == "" || d != m.gitDecision(c.Node, member) {
		return events.ErrConflict
	}
	return nil
}

func gitAnchor(anchor string) bool { return strings.HasPrefix(anchor, "git/") }
