// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"fmt"
	"regexp"
	"strings"
)

// workStateSep matches the spaces and hyphens collapsed by workStateNormSQL.
var workStateSep = regexp.MustCompile(`[[:space:]-]+`)

// normaliseWorkState matches workStateNormSQL: trim, lowercase, and collapse
// each run of spaces or hyphens to one underscore.
func normaliseWorkState(state string) string {
	return workStateSep.ReplaceAllString(strings.ToLower(strings.TrimSpace(state)), "_")
}

// workStateNormSQL is the SQL form of normaliseWorkState for a column or expression.
func workStateNormSQL(expr string) string {
	return `regexp_replace(lower(btrim(` + expr + `)), '[[:space:]-]+', '_', 'g')`
}

// workStateCategoryCTE reads a category from each work kind's field_schema.states
// when those entries carry one. An unknown category is ignored so the fixed
// mapping still applies. Ticket, task and epic catalogs stay separate.
func workStateCategoryCTE() string {
	norm := workStateNormSQL(`elem->>'state'`)
	category := workStateNormSQL(`elem->>'category'`)
	return `configured AS (
                SELECT DISTINCT ON (k.id, ` + norm + `)
                    k.id AS kind_id,
                    ` + norm + ` AS norm,
                    CASE ` + category + `
                        WHEN 'open' THEN 'open'
                        WHEN 'doing' THEN 'in_progress'
                        WHEN 'progress' THEN 'in_progress'
                        WHEN 'in_progress' THEN 'in_progress'
                        WHEN 'done' THEN 'done'
                        WHEN 'cancelled' THEN 'cancelled'
                        WHEN 'canceled' THEN 'cancelled'
                        WHEN 'archived' THEN 'archived'
                        ELSE ''
                    END AS bucket
                FROM node_kinds k
                CROSS JOIN LATERAL jsonb_array_elements(
                    CASE WHEN jsonb_typeof(k.field_schema->'states')='array' THEN k.field_schema->'states' ELSE '[]'::jsonb END
                ) elem
                WHERE k.tenant_id=current_setting('aeon.tenant_id')::uuid
                    AND k.slug IN ('ticket','task','epic')
                    AND jsonb_typeof(elem)='object'
                    AND btrim(coalesce(elem->>'state',''))<>''
                    AND btrim(coalesce(elem->>'category',''))<>''
                ORDER BY k.id, ` + norm + `
            )`
}

// workCountBucketSQL is the bucket for one subtree row. A category on that
// row's kind wins. Otherwise cancelled and canceled are cancelled; accepted,
// delivered and done are done; in_progress (any spelling), active and qa are
// in progress; archived stays out of those four; every other state is open.
func workCountBucketSQL(stateExpr, categoryAlias string) string {
	fixed := `CASE ` + workStateNormSQL(stateExpr) + `
                        WHEN 'cancelled' THEN 'cancelled'
                        WHEN 'canceled' THEN 'cancelled'
                        WHEN 'accepted' THEN 'done'
                        WHEN 'delivered' THEN 'done'
                        WHEN 'done' THEN 'done'
                        WHEN 'in_progress' THEN 'in_progress'
                        WHEN 'inprogress' THEN 'in_progress'
                        WHEN 'active' THEN 'in_progress'
                        WHEN 'qa' THEN 'in_progress'
                        WHEN 'archived' THEN 'archived'
                        ELSE 'open' END`
	return `coalesce(nullif(` + categoryAlias + `.bucket,''), ` + fixed + `)`
}

// workNotClosedSQL is the Hide closed predicate. It uses workCountBucketSQL, so a
// kind's category and a spelling such as canceled stay in step with the counts.
// Done, cancelled and archived are closed; open and in progress stay visible.
func workNotClosedSQL(stateExpr, categoryAlias string) string {
	return workCountBucketSQL(stateExpr, categoryAlias) + ` NOT IN ('done','cancelled','archived')`
}

// workStateKnownSQL is 0 for a workflow state and 1 otherwise. It stays
// ascending in both sort directions, so an unknown spelling stays after the
// workflow. The list is a plain CASE, the same shape as the previous sort.
func workStateKnownSQL(stateExpr string) string {
	return `CASE WHEN ` + stateExpr + ` IN ('open','new','backlog','blocked','in_progress','in-progress','in progress','active','qa','accepted','delivered','done','cancelled','canceled','archived') THEN 0 ELSE 1 END`
}

// workStateOrderSQL ranks a known state in workflow order: open, new, backlog,
// blocked, in progress and active, qa, accepted, delivered, done, cancelled,
// archived. Any other spelling ties and the known/unknown CASE separates it.
func workStateOrderSQL(stateExpr string) string {
	return `CASE ` + stateExpr + ` WHEN 'open' THEN 0 WHEN 'new' THEN 1 WHEN 'backlog' THEN 2 WHEN 'blocked' THEN 3 WHEN 'in_progress' THEN 4 WHEN 'in-progress' THEN 4 WHEN 'in progress' THEN 4 WHEN 'active' THEN 4 WHEN 'qa' THEN 5 WHEN 'accepted' THEN 6 WHEN 'delivered' THEN 7 WHEN 'done' THEN 8 WHEN 'cancelled' THEN 9 WHEN 'canceled' THEN 9 WHEN 'archived' THEN 10 ELSE 11 END`
}

// compileStateCatalog checks field_schema.states: objects with a distinct state
// and a category project counts understand.
func compileStateCatalog(val any) error {
	items, ok := val.([]any)
	if !ok {
		return fmt.Errorf("states must be an array")
	}
	seen := map[string]bool{}
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("states entries must be objects")
		}
		rawState, ok := obj["state"].(string)
		if !ok || strings.TrimSpace(rawState) == "" || len(rawState) > 64 {
			return fmt.Errorf("states entries need a state")
		}
		rawCategory, ok := obj["category"].(string)
		if !ok || strings.TrimSpace(rawCategory) == "" {
			return fmt.Errorf("states entries need a category")
		}
		norm := normaliseWorkState(rawState)
		if norm == "" || seen[norm] {
			return fmt.Errorf("states entries must name a distinct state")
		}
		seen[norm] = true
		switch normaliseWorkState(rawCategory) {
		case "open", "doing", "progress", "in_progress", "done", "cancelled", "canceled", "archived":
		default:
			return fmt.Errorf("states category must be open, doing, in_progress, done, cancelled or archived")
		}
	}
	return nil
}
