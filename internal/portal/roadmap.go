// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/ticketbenefits"
)

const (
	roadmapSchema    = "inspr.release-roadmap.v1"
	roadmapCache     = "public, max-age=60"
	roadmapLaterRank = 1_000_000
	roadmapScanCap   = 500
	roadmapItemCap   = 200
)

// publicRoadmapItem is the whitelist. Ticket ids, keys, titles and internal
// notes are never encoded.
type publicRoadmapItem struct {
	PillEN    string `json:"pill_en"`
	PillDE    string `json:"pill_de"`
	BenefitEN string `json:"benefit_en"`
	BenefitDE string `json:"benefit_de"`
	Target    string `json:"target"`
	Status    string `json:"status"`
}

type publicRoadmapDocument struct {
	Schema  string              `json:"schema"`
	Product *portalProduct      `json:"product"`
	Items   []publicRoadmapItem `json:"items"`
}

type roadmapRow struct {
	item     publicRoadmapItem
	rank     int
	priority int
	id       string
}

var (
	roadmapReleaseTag = regexp.MustCompile(`(?i)^roadmap-r([1-9][0-9]{0,3})$`)
	roadmapLaterTag   = regexp.MustCompile(`(?i)^roadmap-later$`)
	releaseLabelRE    = regexp.MustCompile(`(?i)^(?:release\s+)?r?(\d+)$`)
	laterLabelRE      = regexp.MustCompile(`(?i)^later$`)
)

// loadPublicRoadmap lists approved roadmap tickets for the pace-linked project.
// Release history is not required. A shipped ticket already visible in the
// public release history is omitted so it does not stay in both places.
// A missing hide_from_release_notes is visible; only boolean true hides.
func loadPublicRoadmap(ctx context.Context, tx pgx.Tx) ([]publicRoadmapItem, error) {
	productID, err := portalProductID(ctx, tx)
	if errors.Is(err, errNoProduct) {
		return []publicRoadmapItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	shipped, err := frozenPublicTicketIDs(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT n.id::text,
		       n.state,
		       lower(btrim(coalesce(n.fields->>'priority', ''))),
		       n.fields,
		       CASE WHEN jsonb_typeof(n.fields->'tags') = 'array' THEN n.fields->'tags' ELSE '[]'::jsonb END,
		       (
		         SELECT assigned.sequence FROM (
 SELECT pr.sequence FROM `+delivery.Effective+` placed JOIN project_releases pr ON pr.tenant_id=placed.tenant_id AND pr.release_node_id=placed.release_node_id WHERE placed.tenant_id=n.tenant_id AND placed.item_node_id=n.id AND pr.visibility='published' AND pr.sequence BETWEEN 1 AND 9999
 UNION ALL
 SELECT r.number AS sequence
		         FROM journey_tickets jt
		         JOIN journey_releases r
		           ON r.tenant_id = jt.tenant_id
		          AND r.release_node_id = jt.release_node_id
		          AND r.project_node_id = jt.project_node_id
		          AND r.number BETWEEN 1 AND 9999
		         JOIN nodes rn ON rn.tenant_id = r.tenant_id AND rn.id = r.release_node_id AND rn.deleted_at IS NULL
		         JOIN node_kinds rk ON rk.tenant_id = rn.tenant_id AND rk.id = rn.kind_id AND rk.slug = 'release'
		         WHERE jt.tenant_id = n.tenant_id
		           AND jt.ticket_node_id = n.id
		           AND jt.project_node_id = n.project_id
		         AND NOT EXISTS(SELECT 1 FROM project_delivery d WHERE d.tenant_id=n.tenant_id AND d.project_node_id=n.project_id)
 ) assigned ORDER BY assigned.sequence
		         LIMIT 1
		       ),
		       CASE jsonb_typeof(n.fields->'release')
		         WHEN 'object' THEN coalesce(n.fields->'release'->>'label', n.fields->'release'->>'name')
		         WHEN 'string' THEN n.fields->>'release'
		       END
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id AND k.slug IN ('work','ticket')
		JOIN portal_product_pace pace ON pace.tenant_id = n.tenant_id AND pace.project_node_id = n.project_id
		WHERE pace.product_id=$1::uuid AND n.deleted_at IS NULL
		  AND lower(n.state) NOT IN ('cancelled', 'canceled', 'archived', 'deleted')
		  AND jsonb_typeof(n.fields->'roadmap_public') = 'boolean'
		  AND n.fields->>'roadmap_public' = 'true'
		  AND n.fields->>'roadmap_public_source' = 'person'
		  AND n.fields->>'roadmap_public_by' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		  AND EXISTS (
		      SELECT 1 FROM principals pr
		      WHERE pr.tenant_id = n.tenant_id
		        AND pr.kind = 'person'
		        AND pr.id = (n.fields->>'roadmap_public_by')::uuid
		  )
		  AND NOT COALESCE(
		      jsonb_typeof(n.fields->'hide_from_release_notes') = 'boolean'
		      AND n.fields->>'hide_from_release_notes' = 'true',
		      false
		  )
		  AND EXISTS (
		      SELECT 1
		      FROM jsonb_array_elements(
		          CASE WHEN jsonb_typeof(n.fields->'tags') = 'array' THEN n.fields->'tags' ELSE '[]'::jsonb END
		      ) AS tag
		      WHERE lower(btrim(CASE jsonb_typeof(tag)
		          WHEN 'string' THEN tag #>> '{}'
		          WHEN 'object' THEN tag->>'name'
		          ELSE '' END)) = 'roadmap'
		  )
		ORDER BY n.id
		LIMIT 500`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	picked := make([]roadmapRow, 0)
	for rows.Next() {
		var id, state, priority string
		var fields, tags []byte
		var number *int
		var label *string
		if err := rows.Scan(&id, &state, &priority, &fields, &tags, &number, &label); err != nil {
			return nil, err
		}
		if _, moved := shipped[strings.ToLower(id)]; moved {
			continue
		}
		item, rank, ok := projectRoadmapItem(id, state, fields, tags, number, label)
		if !ok {
			continue
		}
		picked = append(picked, roadmapRow{item: item, rank: rank, priority: roadmapPriority(priority), id: strings.ToLower(id)})
		if len(picked) >= roadmapScanCap {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(picked, func(i, j int) bool {
		if picked[i].rank != picked[j].rank {
			return picked[i].rank < picked[j].rank
		}
		if picked[i].priority != picked[j].priority {
			return picked[i].priority < picked[j].priority
		}
		return picked[i].id < picked[j].id
	})
	if len(picked) > roadmapItemCap {
		picked = picked[:roadmapItemCap]
	}
	out := make([]publicRoadmapItem, 0, len(picked))
	for _, row := range picked {
		out = append(out, row.item)
	}
	return out, nil
}

func projectRoadmapItem(id, state string, fields, tags []byte, number *int, label *string) (publicRoadmapItem, int, bool) {
	if !uuidPattern.MatchString(id) {
		return publicRoadmapItem{}, 0, false
	}
	if len(ticketbenefits.Issues(fields)) > 0 {
		return publicRoadmapItem{}, 0, false
	}
	var text struct {
		PillEN    string `json:"pill_en"`
		PillDE    string `json:"pill_de"`
		BenefitEN string `json:"benefit_en"`
		BenefitDE string `json:"benefit_de"`
	}
	if json.Unmarshal(fields, &text) != nil {
		return publicRoadmapItem{}, 0, false
	}
	item := publicRoadmapItem{
		PillEN:    publicLine(text.PillEN, 80),
		PillDE:    publicLine(text.PillDE, 80),
		BenefitEN: publicLine(text.BenefitEN, 600),
		BenefitDE: publicLine(text.BenefitDE, 600),
		Status:    roadmapStatus(state),
	}
	if item.PillEN == "" || item.PillDE == "" || item.BenefitEN == "" || item.BenefitDE == "" {
		return publicRoadmapItem{}, 0, false
	}
	releaseLabel := ""
	if label != nil {
		releaseLabel = *label
	}
	target, rank, ok := publicRoadmapTarget(tagNames(tags), number, releaseLabel)
	if !ok {
		return publicRoadmapItem{}, 0, false
	}
	item.Target = target
	return item, rank, true
}

// publicRoadmapTarget resolves rN or later. A journey release number wins over
// a target tag. The release title is never a target.
func publicRoadmapTarget(tags []string, releaseNumber *int, releaseLabel string) (string, int, bool) {
	if releaseNumber != nil && *releaseNumber >= 1 && *releaseNumber <= 9999 {
		return "r" + strconv.Itoa(*releaseNumber), *releaseNumber, true
	}
	best := 0
	for _, tag := range tags {
		match := roadmapReleaseTag.FindStringSubmatch(strings.TrimSpace(tag))
		if match == nil {
			continue
		}
		n, err := strconv.Atoi(match[1])
		if err != nil || n < 1 || n > 9999 {
			continue
		}
		if best == 0 || n < best {
			best = n
		}
	}
	if best > 0 {
		return "r" + strconv.Itoa(best), best, true
	}
	for _, tag := range tags {
		if roadmapLaterTag.MatchString(strings.TrimSpace(tag)) {
			return "later", roadmapLaterRank, true
		}
	}
	label := strings.TrimSpace(releaseLabel)
	if laterLabelRE.MatchString(label) {
		return "later", roadmapLaterRank, true
	}
	match := releaseLabelRE.FindStringSubmatch(label)
	if match == nil {
		return "", 0, false
	}
	digits := match[1]
	if len(digits) > 1 && digits[0] == '0' {
		return "", 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 || n > 9999 {
		return "", 0, false
	}
	return "r" + strconv.Itoa(n), n, true
}

func tagNames(raw []byte) []string {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		var text string
		if json.Unmarshal(item, &text) == nil {
			names = append(names, strings.TrimSpace(text))
			continue
		}
		var object struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(item, &object) == nil && strings.TrimSpace(object.Name) != "" {
			names = append(names, strings.TrimSpace(object.Name))
		}
	}
	return names
}

func roadmapStatus(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "done", "accepted", "delivered":
		return "shipped"
	case "in_progress", "in-progress", "active", "qa":
		return "in_progress"
	default:
		return "planned"
	}
}

func roadmapPriority(priority string) int {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3
	}
}

// frozenPublicTicketIDs is the set already shown on the public release history.
// History that is off contributes nothing, so a shipped roadmap item stays
// visible instead of disappearing from the public site.
func frozenPublicTicketIDs(ctx context.Context, tx pgx.Tx) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	on, err := portalPublishesReleases(ctx, tx)
	if err != nil || !on {
		return out, err
	}
	snaps, err := listFrozenSnapshots(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, snap := range snaps {
		if _, ok := projectPublicRelease(snap.version, snap.at, snap.raw); !ok {
			continue
		}
		var parsed releaseSnapshot
		if json.Unmarshal(snap.raw, &parsed) != nil {
			continue
		}
		for _, ticket := range parsed.Tickets {
			if _, ok := visiblePublicNote(ticket); !ok {
				continue
			}
			id := strings.ToLower(strings.TrimSpace(ticket.ID))
			if uuidPattern.MatchString(id) {
				out[id] = struct{}{}
			}
		}
	}
	return out, nil
}
