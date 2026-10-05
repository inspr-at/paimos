// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// classicRelation is one stored classic link, already resolved to Aeon nodes.
// Empty endpoint ids mean that classic issue was not imported.
type classicRelation struct {
	Type         string
	SourceNodeID string
	TargetNodeID string
	ClassicRef   string
}

// mapClassicRelation returns the node_relations type for a classic type.
// mode "dependency" reverses depends_on so the dependency blocks the dependent.
// mode "undirected" is stored as relates, whose endpoints are ordered by UUID.
func mapClassicRelation(typ string) (mapped, mode string, ok bool) {
	switch typ {
	case "depends_on":
		return "blocks", "dependency", true
	case "blocks":
		return "blocks", "direct", true
	case "relates", "related", "follows_from", "impacts", "applies_to_memory", "groups":
		return "relates", "undirected", true
	case "duplicates":
		return "duplicates", "direct", true
	case "cites":
		return "cites", "direct", true
	default:
		return "", "", false
	}
}

// applyClassicRelation writes the native projection for one classic relation.
// The returned count is rows inserted or updated. An already-applied relation
// returns zero. The classic type is recorded on the event, not overwritten.
func applyClassicRelation(ctx context.Context, tx pgx.Tx, tenantID, actor string, rel classicRelation) (int, error) {
	switch rel.Type {
	case "parent":
		if rel.SourceNodeID == "" || rel.TargetNodeID == "" || rel.SourceNodeID == rel.TargetNodeID {
			return 0, nil
		}
		// Classic parent rows use source = parent, target = child.
		changed, err := setParent(ctx, tx, tenantID, actor, rel.TargetNodeID, rel.SourceNodeID)
		if err != nil || !changed {
			return 0, err
		}
		return 1, nil
	case "release":
		return applyReleaseMembership(ctx, tx, tenantID, actor, rel)
	}
	mapped, mode, ok := mapClassicRelation(rel.Type)
	if !ok || rel.SourceNodeID == "" || rel.TargetNodeID == "" {
		return 0, nil
	}
	src, dst := rel.SourceNodeID, rel.TargetNodeID
	if mode == "dependency" {
		src, dst = dst, src
	}
	if mode == "undirected" {
		var orderedSrc, orderedDst string
		if err := tx.QueryRow(ctx, `SELECT LEAST($1::uuid, $2::uuid)::text, GREATEST($1::uuid, $2::uuid)::text`, src, dst).Scan(&orderedSrc, &orderedDst); err != nil {
			return 0, err
		}
		src, dst = orderedSrc, orderedDst
	}
	if src == dst {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,source_node_id,target_node_id,type) DO NOTHING`, tenantID, src, dst, mapped)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, nil
	}
	var raw json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(node_relations) || jsonb_build_object('classic_type',$5::text,'classic_ref',$6::text) FROM node_relations WHERE tenant_id=$1 AND source_node_id=$2 AND target_node_id=$3 AND type=$4`, tenantID, src, dst, mapped, rel.Type, rel.ClassicRef).Scan(&raw); err != nil {
		return 0, err
	}
	nodeID := src
	if _, err := events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actor}, events.Change{
		NodeID: &nodeID, Type: "relation.created", After: raw,
	}); err != nil {
		return 0, err
	}
	return 1, nil
}

type liveNode struct {
	Kind        string
	ClassicType string
	Deleted     bool
}

func lookupNode(ctx context.Context, tx pgx.Tx, tenantID, id string) (liveNode, error) {
	if id == "" {
		return liveNode{}, nil
	}
	var n liveNode
	err := tx.QueryRow(ctx, `SELECT k.slug,coalesce(n.fields->'classic'->>'type',
 (SELECT e.after->'fields'->'classic'->>'type' FROM events e
  WHERE e.tenant_id=n.tenant_id AND e.node_id=n.id AND e.type IN ('import.node_created','import.node_updated')
  ORDER BY e.id DESC LIMIT 1),''),n.deleted_at IS NOT NULL
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1 AND n.id=$2`, tenantID, id).Scan(&n.Kind, &n.ClassicType, &n.Deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return liveNode{}, nil
	}
	return n, err
}

// applyReleaseMembership registers the release on its project and points a
// ticket member at it. Classic PPM stores source_id = release container and
// target_id = member issue. A row stored the other way is accepted when the
// node kinds identify the release. current_release_node_id is left untouched,
// and imported releases stay in planning: released state would require a
// released_at this relation does not carry.
func applyReleaseMembership(ctx context.Context, tx pgx.Tx, tenantID, actor string, rel classicRelation) (int, error) {
	source, err := lookupNode(ctx, tx, tenantID, rel.SourceNodeID)
	if err != nil {
		return 0, err
	}
	target, err := lookupNode(ctx, tx, tenantID, rel.TargetNodeID)
	if err != nil {
		return 0, err
	}
	releaseID, memberID := rel.SourceNodeID, rel.TargetNodeID
	release, member := source, target
	if release.Kind != "release" && member.Kind == "release" {
		releaseID, memberID = memberID, releaseID
		release, member = member, release
	}
	if releaseID == "" || release.Kind != "release" || release.Deleted {
		return 0, nil
	}
	projectID, err := releaseProjectNode(ctx, tx, tenantID, releaseID)
	if err != nil {
		return 0, err
	}
	if projectID == "" {
		return 0, nil
	}
	adopted, err := delivery.ReleasesMode(ctx, tx, projectID)
	if err != nil {
		return 0, err
	}
	if adopted {
		return 0, errReleasesModeSkipped
	}
	wrote := 0
	projectWrote, err := ensureJourneyProject(ctx, tx, tenantID, projectID)
	if err != nil {
		return 0, err
	}
	wrote += projectWrote
	var locked string
	err = tx.QueryRow(ctx, `SELECT project_node_id::text FROM journey_projects WHERE tenant_id=$1 AND project_node_id=$2 FOR UPDATE`, tenantID, projectID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return wrote, nil
	}
	if err != nil {
		return 0, err
	}
	releaseWrote, err := ensureJourneyRelease(ctx, tx, tenantID, projectID, releaseID)
	if err != nil {
		return 0, err
	}
	wrote += releaseWrote
	linked := false
	// Preserve the classic journey projection's ticket membership semantics;
	// the unified native kind does not erase source-type provenance. Parent
	// placement and current-leaf release membership belong to AEON-652.
	journeyTicket := member.Kind == "ticket" || member.Kind == "work" && member.ClassicType == "ticket"
	if memberID != "" && journeyTicket && !member.Deleted && memberID != releaseID {
		ticketWrote, err := linkTicketRelease(ctx, tx, tenantID, projectID, memberID, releaseID)
		if err != nil {
			return 0, err
		}
		wrote += ticketWrote
		linked = ticketWrote > 0
	}
	if wrote == 0 {
		return 0, nil
	}
	payload := Record{
		"classic_ref":     rel.ClassicRef,
		"classic_type":    "release",
		"project_node_id": projectID,
		"release_node_id": releaseID,
	}
	nodeID := releaseID
	if linked {
		payload["ticket_node_id"] = memberID
		nodeID = memberID
	}
	if _, err := events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actor}, events.Change{
		NodeID: &nodeID, Type: "import.release_membership", After: payload,
	}); err != nil {
		return 0, err
	}
	return wrote, nil
}

func releaseProjectNode(ctx context.Context, tx pgx.Tx, tenantID, releaseNodeID string) (string, error) {
	var classic string
	err := tx.QueryRow(ctx, `
		SELECT e.after->'fields'->'classic'->>'project_id'
		FROM events e
		WHERE e.tenant_id=$1 AND e.node_id=$2
		  AND e.type IN ('import.node_created','import.node_updated')
		  AND coalesce(e.after->'fields'->'classic'->>'project_id','') ~ '^[0-9]+$'
		ORDER BY e.id DESC
		LIMIT 1`, tenantID, releaseNodeID).Scan(&classic)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if classic != "" {
		var projectID string
		err = tx.QueryRow(ctx, `
			SELECT n.id::text
			FROM events e
			JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id
			JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
			WHERE e.tenant_id=$1
			  AND e.type IN ('import.node_created','import.node_updated')
			  AND k.slug='project'
			  AND n.deleted_at IS NULL
			  AND e.after->'fields'->'classic'->>'id'=$2
			ORDER BY e.id DESC
			LIMIT 1`, tenantID, classic).Scan(&projectID)
		if err == nil {
			return projectID, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	var projectID string
	err = tx.QueryRow(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT id, parent_id, 0 AS depth FROM nodes WHERE tenant_id=$1 AND id=$2
			UNION ALL
			SELECT n.id, n.parent_id, a.depth+1
			FROM nodes n
			JOIN ancestors a ON n.tenant_id=$1 AND n.id=a.parent_id
			WHERE a.depth < 64
		)
		SELECT a.id::text
		FROM ancestors a
		JOIN nodes n ON n.tenant_id=$1 AND n.id=a.id
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE k.slug='project' AND n.deleted_at IS NULL
		ORDER BY a.depth
		LIMIT 1`, tenantID, releaseNodeID).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return projectID, err
}

func ensureJourneyProject(ctx context.Context, tx pgx.Tx, tenantID, projectID string) (int, error) {
	project, err := lookupNode(ctx, tx, tenantID, projectID)
	if err != nil {
		return 0, err
	}
	if project.Kind != "project" || project.Deleted {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `SELECT aeon_seed_requirement_kind($1::uuid)`, tenantID); err != nil {
		return 0, err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO journey_projects (tenant_id, project_node_id)
		VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (tenant_id, project_node_id) DO NOTHING
		RETURNING project_node_id::text`, tenantID, projectID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func ensureJourneyRelease(ctx context.Context, tx pgx.Tx, tenantID, projectID, releaseID string) (int, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO journey_releases (tenant_id, release_node_id, project_node_id, number, state)
		SELECT $1::uuid, $2::uuid, $3::uuid,
		       coalesce((SELECT max(number) FROM journey_releases WHERE tenant_id=$1 AND project_node_id=$3), 0) + 1,
		       'planning'
		WHERE NOT EXISTS (
			SELECT 1 FROM journey_releases WHERE tenant_id=$1 AND release_node_id=$2
		)
		RETURNING release_node_id::text`, tenantID, releaseID, projectID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("register release: %w", err)
	}
	return 1, nil
}

func linkTicketRelease(ctx context.Context, tx pgx.Tx, tenantID, projectID, ticketID, releaseID string) (int, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO journey_tickets (
			tenant_id, ticket_node_id, project_node_id, release_node_id, walker_position, source)
		SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid,
		       coalesce((SELECT max(walker_position)+1 FROM journey_tickets WHERE tenant_id=$1 AND project_node_id=$3), 0),
		       'manual'
		ON CONFLICT (tenant_id, ticket_node_id) DO UPDATE
			SET release_node_id = EXCLUDED.release_node_id
			WHERE journey_tickets.project_node_id = EXCLUDED.project_node_id
			  AND journey_tickets.release_node_id IS NULL
		RETURNING ticket_node_id::text`, tenantID, ticketID, projectID, releaseID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("link ticket release: %w", err)
	}
	return 1, nil
}

var errReleasesModeSkipped = errors.New("This project plans with releases")
