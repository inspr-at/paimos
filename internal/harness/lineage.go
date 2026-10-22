// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/runkind"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

func registrationParent(s Session) *string {
	if s.lineageParentID != nil {
		return s.lineageParentID
	}
	return s.ParentID
}

// Project only after writes/events. Event snapshots must not carry an ancestor
// into a child project's audit stream for viewers who cannot read the parent.
func projectLineage(s Session) Session {
	if s.lineageParentVisible {
		s.CrossProjectParentID = s.lineageParentID
	}
	return s
}

// Discover only a visible parent, then take every project fence in one order.
// The caller already holds the tenant and work-tree fences. No project or
// session ownership changes between discovery and the final validation.
func lockRegistrationHierarchy(ctx context.Context, tx pgx.Tx, projectID string, parentID *string) (string, error) {
	parentProject := projectID
	if parentID != nil {
		if !workorders.UUID(*parentID) {
			return "", workorders.Fail(400, "invalid parent session")
		}
		err := tx.QueryRow(ctx, `SELECT project_id::text FROM harness_sessions WHERE id=$1`, *parentID).Scan(&parentProject)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", workorders.Fail(409, "active visible parent required")
		}
		if err != nil {
			return "", err
		}
	}
	projects := []string{projectID}
	if parentProject != projectID {
		projects = append(projects, parentProject)
	}
	sort.Strings(projects)
	for _, id := range projects {
		if err := lockHierarchy(ctx, tx, id); err != nil {
			return "", err
		}
	}
	return parentProject, nil
}

func canonicalLineagePerson(ctx context.Context, tx pgx.Tx, id *string) (*string, error) {
	if id == nil {
		return nil, workorders.Fail(409, "verified canonical person required for cross-project lineage")
	}
	var canonical string
	err := tx.QueryRow(ctx, `SELECT c.id::text FROM principals p JOIN principals c ON c.tenant_id=p.tenant_id AND c.id=coalesce(p.linked_to,p.id)
 WHERE p.id=$1 AND p.kind='person' AND c.kind='person' AND c.status='active' AND c.linked_to IS NULL`, *id).Scan(&canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(409, "verified canonical person required for cross-project lineage")
	}
	return &canonical, err
}

// A parent link is attribution, never a person resolver for the child. A run's
// immutable starting person or the exact key's explicit person binding is
// required; key creators, machines and transport principals are not fallbacks.
func validateCrossProjectParent(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, parentProject string, in registration, owner *string, childID string) (*string, error) {
	permission := "harness.write"
	if p.Kind == tenant.Agent {
		permission = "harness.worker"
	}
	if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: projectID}); err != nil {
		return nil, err
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{ProjectID: parentProject}); err != nil {
		return nil, err
	}
	if err := project(ctx, tx, parentProject); err != nil {
		return nil, err
	}
	parent, err := load(ctx, tx, parentProject, *in.ParentID, true)
	if err != nil {
		return nil, err
	}
	if parent.StoppedAt != nil || parent.ArchivedAt != nil {
		return nil, workorders.Fail(409, "active visible parent required")
	}
	expired, err := heartbeatExpired(ctx, tx, parent)
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, workorders.Fail(409, "active visible parent required")
	}
	if runkind.Process(in.Harness) && parent.Role != "coordinator" {
		return nil, workorders.Fail(400, "--parent-session must name a visible coordinator")
	}
	if in.RunID == nil && p.Kind == tenant.Agent {
		owner = nil
		// Read the current exact key, not a cached middleware identity.
		if err := tx.QueryRow(ctx, `SELECT person_id::text FROM agent_keys WHERE tenant_id=$1 AND id=$2 AND principal_id=$3 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp())`, p.TenantID, p.AuthKeyID, p.ID).Scan(&owner); err != nil {
			return nil, workorders.Fail(403, "explicit key person required for cross-project lineage")
		}
	}
	person, err := canonicalLineagePerson(ctx, tx, owner)
	if err != nil {
		return nil, err
	}
	parentPerson := parent.OwnerPrincipalID
	if parent.RunID != nil {
		if err := tx.QueryRow(ctx, `SELECT acting_person_id::text FROM agent_runs WHERE tenant_id=$1 AND id=$2`, p.TenantID, *parent.RunID).Scan(&parentPerson); err != nil {
			return nil, err
		}
	}
	parentPerson, err = canonicalLineagePerson(ctx, tx, parentPerson)
	if err != nil {
		return nil, err
	}
	if *person != *parentPerson {
		return nil, workorders.Fail(409, "run_person_conflict")
	}
	if err = validateLineageGraph(ctx, tx, *in.ParentID, childID); err != nil {
		return nil, err
	}
	return person, nil
}

// Follow both representations, at most 17 levels. Missing ancestor visibility
// is a refusal, never an invitation to fetch context using broader authority.
func validateLineageGraph(ctx context.Context, tx pgx.Tx, parentID, childID string) error {
	if parentID == childID {
		return workorders.Fail(400, "invalid parent session")
	}
	var depth int
	var cycle, missing bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE edges AS NOT MATERIALIZED (
 SELECT h.id,coalesce(h.parent_id,l.parent_session_id) AS parent_id FROM harness_sessions h
 LEFT JOIN harness_session_lineage l ON l.tenant_id=h.tenant_id AND l.child_session_id=h.id
), ancestors AS (
 SELECT id,parent_id,1 AS depth FROM edges WHERE id=$1
 UNION ALL SELECT e.id,e.parent_id,a.depth+1 FROM edges e JOIN ancestors a ON e.id=a.parent_id WHERE a.depth<17
) SELECT coalesce(max(depth),0),coalesce(bool_or(id=$2::uuid),false),
 coalesce(bool_or(parent_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM edges e WHERE e.id=ancestors.parent_id)),false) FROM ancestors`, parentID, nullable(childID)).Scan(&depth, &cycle, &missing)
	if err != nil {
		return err
	}
	if cycle {
		return workorders.Fail(409, "harness hierarchy cycle")
	}
	if missing {
		return workorders.Fail(409, "visible harness hierarchy required")
	}
	if depth >= 16 {
		return workorders.Fail(409, "harness hierarchy exceeds 16 ancestors")
	}
	if childID != "" {
		var below, count int
		var hidden bool
		err = tx.QueryRow(ctx, `WITH RECURSIVE edges AS NOT MATERIALIZED (
 SELECT h.id,coalesce(h.parent_id,l.parent_session_id) AS parent_id FROM harness_sessions h
 LEFT JOIN harness_session_lineage l ON l.tenant_id=h.tenant_id AND l.child_session_id=h.id
), descendants AS (
 SELECT id,1 AS depth FROM edges WHERE id=$1
 UNION ALL SELECT e.id,d.depth+1 FROM edges e JOIN descendants d ON e.parent_id=d.id WHERE d.depth<17
), bounded AS MATERIALIZED (SELECT * FROM descendants LIMIT 4097)
 SELECT coalesce(max(depth),0),count(*),EXISTS(SELECT 1 FROM harness_session_lineage l JOIN bounded d ON l.parent_session_id=d.id
 WHERE NOT EXISTS(SELECT 1 FROM edges e WHERE e.id=l.child_session_id)) FROM bounded`, childID).Scan(&below, &count, &hidden)
		if err != nil {
			return err
		}
		if hidden {
			return workorders.Fail(409, "visible harness hierarchy required")
		}
		if count > 4096 {
			return workorders.Fail(409, "harness hierarchy too large to move")
		}
		if depth+below > 16 {
			return workorders.Fail(409, "harness hierarchy exceeds 16 ancestors")
		}
	}
	return nil
}
