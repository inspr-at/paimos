// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/workquery"
)

// workStateSep matches the spaces and hyphens collapsed by workStateNormSQL.
var workStateSep = regexp.MustCompile(`[[:space:]-]+`)

// normaliseWorkState matches workStateNormSQL: trim, lowercase, and collapse
// each run of spaces or hyphens to one underscore.
func normaliseWorkState(state string) string {
	return workStateSep.ReplaceAllString(strings.ToLower(strings.TrimSpace(state)), "_")
}

func workStateNormSQL(expr string) string          { return workquery.StateNormSQL(expr) }
func workStateCategoryCTE() string                 { return workquery.StateCategoryCTE() }
func workCountBucketSQL(expr, alias string) string { return workquery.StateBucketSQL(expr, alias) }
func workNotClosedSQL(expr, alias string) string {
	return workCountBucketSQL(expr, alias) + ` NOT IN ('done','cancelled','archived')`
}

// workStateKnownSQL is 0 for a workflow state and 1 otherwise. It stays
// ascending in both sort directions, so an unknown spelling stays after the
// workflow. The state is normalised the same way as the buckets, so " OPEN ",
// " QA " and "in--progress" stay in the workflow.
func workStateKnownSQL(stateExpr string) string {
	return `CASE WHEN ` + workStateNormSQL(stateExpr) + ` IN ('open','new','backlog','blocked','in_progress','active','qa','accepted','delivered','done','cancelled','canceled','archived') THEN 0 ELSE 1 END`
}

// workStateOrderSQL ranks a known state in workflow order: new, backlog, open,
// blocked, in progress and active, qa, done, delivered, accepted, cancelled,
// archived. Any other spelling ties and the known/unknown CASE separates it.
// Ranking reads the normalised state, so spaced and hyphenated spellings share
// a rank with the canonical word.
func workStateOrderSQL(stateExpr string) string {
	return `CASE ` + workStateNormSQL(stateExpr) + ` WHEN 'new' THEN 0 WHEN 'backlog' THEN 1 WHEN 'open' THEN 2 WHEN 'blocked' THEN 3 WHEN 'in_progress' THEN 4 WHEN 'active' THEN 4 WHEN 'qa' THEN 5 WHEN 'done' THEN 6 WHEN 'delivered' THEN 7 WHEN 'accepted' THEN 8 WHEN 'cancelled' THEN 9 WHEN 'canceled' THEN 9 WHEN 'archived' THEN 10 ELSE 11 END`
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
