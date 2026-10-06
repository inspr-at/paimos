// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Suggestions are deterministic planning hints, never confirmed estimates of
// difficulty. An estimate or role change with valid hours opts into filling gaps.
func suggestEstimateRoute(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, title string, parent *string, raw, before json.RawMessage) (json.RawMessage, error) {
	if kind != "work" && kind != "ticket" && kind != "task" {
		return raw, nil
	}
	fields, err := decodeRouteFields(raw)
	if err != nil {
		return nil, err
	}
	hours, ok := estimateNumber(fields["estimate_hours"])
	if !ok {
		return raw, nil
	}
	old, err := decodeRouteFields(before)
	if err != nil {
		return nil, err
	}
	// The update caller supplies the stored fields read under the node lock,
	// after canonicalizing the incoming estimate and role. A replacement
	// document carrying unchanged values must not backfill missing hints.
	if sameEstimateFields(fields, old) {
		sameRole, err := sameRouteValue(fields, old, "route_role")
		if err != nil || sameRole {
			return raw, err
		}
	}
	project, err := routeProject(ctx, tx, parent)
	if err != nil {
		return nil, err
	}
	role, area := routeHints(title, fields)
	missing := func(group routeGroup) bool {
		value, set, _ := routeText(fields, group.value)
		return !set || !group.known(value)
	}
	// A nearest ancestor's configured defaults beat its title hints. Reads
	// stay in the caller's tenant and project visibility; hidden parents add
	// no evidence. The depth bound also handles malformed legacy trees.
	if parent != nil && (missing(routeGroups[0]) || missing(routeGroups[1])) {
		rows, err := tx.Query(ctx, `WITH RECURSIVE ancestors AS (
		 SELECT id,parent_id,title,fields,0 AS depth FROM nodes
		 WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND id=$1 AND deleted_at IS NULL
		 UNION ALL SELECT n.id,n.parent_id,n.title,n.fields,a.depth+1 FROM nodes n JOIN ancestors a ON n.id=a.parent_id
		 WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.deleted_at IS NULL AND a.depth<32
		) SELECT title,fields FROM ancestors ORDER BY depth`, *parent)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ancestorTitle string
			var ancestorRaw json.RawMessage
			if err := rows.Scan(&ancestorTitle, &ancestorRaw); err != nil {
				rows.Close()
				return nil, err
			}
			ancestor, err := decodeRouteFields(ancestorRaw)
			if err != nil {
				rows.Close()
				return nil, err
			}
			r, a := routeHints(ancestorTitle, ancestor)
			if v, set, _ := routeText(ancestor, "route_role"); set && routeGroups[0].known(v) {
				r = v
			}
			if v, set, _ := routeText(ancestor, "area"); set && routeGroups[1].known(v) {
				a = v
			}
			if role == "" {
				role = r
			}
			if area == "" {
				area = a
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if role == "" {
		role = "build"
	}
	if area == "" {
		area = "backend"
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	suggest := func(group routeGroup, value string) {
		fields[group.value], fields[group.source], fields[group.by], fields[group.at] = value, "suggested", p.ID, at
		fields[group.value+"_confirmed"] = false
	}
	if missing(routeGroups[0]) {
		suggest(routeGroups[0], role)
	}
	if missing(routeGroups[1]) {
		known, err := modelregistry.KnownRouteArea(ctx, tx, area, project)
		if err != nil {
			return nil, err
		}
		if known {
			suggest(routeGroups[1], area)
		}
	}
	oldHours, _ := estimateNumber(old["estimate_hours"])
	derived := fields["complexity_source"] == "suggested" && fields["complexity"] == old["complexity"]
	if missing(routeGroups[2]) || (derived && (hours != oldHours || fields["route_role"] != old["route_role"])) {
		suggest(routeGroups[2], estimateComplexity(hours, fields["route_role"].(string)))
	}
	return json.Marshal(fields)
}

// <=2h: S, <=8h: M, >8h: L. build-hard promotes one bucket,
// saturating at L. This is a suggestion rule, not measured model performance.
func estimateComplexity(hours float64, role string) string {
	bucket := 0
	if hours > 2 {
		bucket = 1
	}
	if hours > 8 {
		bucket = 2
	}
	if role == "build-hard" && bucket < 2 {
		bucket++
	}
	return []string{"S", "M", "L"}[bucket]
}

func routeHints(title string, fields map[string]any) (role, area string) {
	text := strings.ToLower(title)
	for _, key := range []string{"tags", "labels"} {
		if labels, ok := fields[key].([]any); ok {
			for _, label := range labels {
				if value, ok := label.(string); ok {
					text += " " + strings.ToLower(value)
				}
			}
		}
	}
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		words[word] = true
	}
	has := func(keys ...string) bool {
		for _, key := range keys {
			if words[key] {
				return true
			}
		}
		return false
	}
	backend := has("backend", "api", "sql", "database", "postgres", "migration", "schema", "rls", "auth", "concurrency")
	frontend := has("frontend", "ui", "vue", "css", "browser", "web", "accessibility")
	switch {
	case has("security", "auth", "permissions", "secrets", "sanitising", "sanitizing", "rls"):
		area = "security"
	case backend && frontend || has("fullstack") || strings.Contains(text, "full-stack"):
		area = "full-stack"
	case backend:
		area = "backend"
	case frontend:
		area = "frontend"
	case has("infra", "nix", "deployment", "deploy", "ci", "docker", "terraform"):
		area = "infra"
	case has("docs", "documentation", "readme", "runbook"):
		area = "docs"
	case has("design", "branding", "typography"):
		area = "design"
	}
	switch {
	case has("security", "auth", "rls", "concurrency", "migration", "schema") || strings.Contains(text, "build-hard"):
		role = "build-hard"
	case has("research", "investigate", "survey", "scout"):
		role = "scout"
	case area == "docs" || has("rename", "mechanical"):
		role = "mechanical"
	}
	return role, area
}
