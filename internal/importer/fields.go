// SPDX-License-Identifier: AGPL-3.0-only
package importer

// Project counts and derived display values are intentionally skipped. All
// other fetched project and issue values remain verbatim in fields.classic.
var skippedProjectFields = map[string]bool{
	"active_issue_count": true, "done_issue_count": true, "issue_count": true,
	"open_issue_count": true, "effective_rate_hourly": true,
	"effective_rate_lp": true, "last_activity": true, "node_depth": true,
	"rate_inherited": true,
}

var explicitIssueFields = []string{
	"acceptance_criteria", "notes", "priority", "tags", "estimate_hours",
	"estimate_lp", "budget_hours", "total_budget", "start_date", "end_date",
	"release", "sprint_ids", "needs_review", "archived", "accepted_at", "hide_from_release_notes",
}

// knowledgeTypes are the classic issue types the knowledge module serves. It
// finds an entry by fields.slug and reads fields.metadata (AEON-138).
var knowledgeTypes = map[string]bool{
	"memory": true, "runbook": true, "guideline": true,
	"external_system": true, "related_project": true,
}

func mappedFields(original, refs Record, sourceID string, project bool) Record {
	classic := Record{"source_id": sourceID}
	for name, value := range original {
		if project && skippedProjectFields[name] {
			continue
		}
		classic[name] = value
	}
	fields := Record{"classic": classic}
	if project {
		if key := stringField(original, "key"); key != "" {
			fields["project_key"] = key
		}
		if v, ok := original["tags"]; ok {
			fields["tags"] = v
		}
		if v, ok := refs["product_owner"]; ok {
			fields["product_owner"] = v
		}
		return fields
	}
	for _, name := range explicitIssueFields {
		if v, ok := original[name]; ok {
			fields[name] = v
		}
	}
	for field, name := range map[string]string{
		"assignee_id": "assignee", "created_by": "created_by",
		"accepted_by": "accepted_by",
	} {
		if v, ok := refs[field]; ok {
			fields[name] = v
		}
	}
	if knowledgeTypes[canonicalType(stringField(original, "type"))] {
		if slug := stringField(original, "slug"); slug != "" {
			fields["slug"] = slug
		}
		if metadata, ok := original["metadata"].(map[string]any); ok && len(metadata) > 0 {
			fields["metadata"] = metadata
		}
	}
	return fields
}
