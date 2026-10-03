// SPDX-License-Identifier: AGPL-3.0-only
// Package workquery shares bounded work predicates between ticket and delivery reads.
package workquery

import (
	"fmt"
	"github.com/inspr-at/paimos/internal/deliverymodel"
	"slices"
	"time"
)

type SortKey struct {
	Name string `json:"name"`
	Desc bool   `json:"desc"`
}
type Query struct {
	KindID      *string   `json:"kind_id"`
	KindsNot    []string  `json:"kinds_not,omitempty"`
	Kinds       []string  `json:"kinds"`
	States      []string  `json:"states"`
	Priorities  []string  `json:"priorities"`
	Assignees   []string  `json:"assignees"`
	Q           string    `json:"q"`
	Within      *string   `json:"within"`
	ParentSet   bool      `json:"parent_set"`
	ParentID    *string   `json:"parent_id"`
	Descendants bool      `json:"descendants"`
	HideClosed  bool      `json:"hide_closed"`
	Sort        []SortKey `json:"sort"`
	FacetNames  []string  `json:"facets"`
	Limit       int       `json:"limit"`
	Cursor      string    `json:"-"`
	// A "!" before a value excludes it: within a dimension the plain values
	// are alternatives (OR) and every excluded value must not match (AND NOT);
	// dimensions combine with AND. "none" stands for an empty value.
	StatesNot     []string `json:"states_not,omitempty"`
	PrioritiesNot []string `json:"priorities_not,omitempty"`
	AssigneesNot  []string `json:"assignees_not,omitempty"`
	// Tags, cost units and releases match by name or label, case-insensitively.
	Tags           []string `json:"tags,omitempty"`
	TagsNot        []string `json:"tags_not,omitempty"`
	CostUnits      []string `json:"cost_units,omitempty"`
	CostUnitsNot   []string `json:"cost_units_not,omitempty"`
	Releases       []string `json:"releases,omitempty"`
	ReleasesNot    []string `json:"releases_not,omitempty"`
	ShipsIn        []string `json:"ships_in,omitempty"`
	ShipsInNot     []string `json:"ships_in_not,omitempty"`
	HumanChecks    []string `json:"human_checks,omitempty"`
	HumanChecksNot []string `json:"human_checks_not,omitempty"`
	// Epics match everything below an epic (its subtree, not the epic itself);
	// "none" is work under no epic.
	Epics    []string `json:"epics,omitempty"`
	EpicsNot []string `json:"epics_not,omitempty"`
	// One date field with an inclusive start and exclusive end instant. Dates
	// kept in fields (start, end, accepted) compare by the calendar day of the
	// bounds in their own offset, which is the caller's local day.
	DateField string     `json:"date_field,omitempty"`
	DateFrom  *time.Time `json:"date_from,omitempty"`
	DateTo    *time.Time `json:"date_to,omitempty"`
	// Only these nodes, with every other filter still applied: a live list
	// refetches the rows that changed through its own query (AEON-326).
	IDs []string `json:"ids,omitempty"`
}

// AssigneeJoin resolves the stored assignee of node n for bounded page summaries.
// Native values (including null unassignment) take precedence over imports.
const AssigneeJoin = ` LEFT JOIN LATERAL (
    SELECT CASE
            WHEN n.fields ? 'assignee' THEN coalesce(n.fields->'assignee'->>'id',n.fields->>'assignee')
            WHEN n.fields ? 'assignee_id' THEN n.fields->>'assignee_id' END AS native,
        NOT (n.fields ? 'assignee' OR n.fields ? 'assignee_id') AS classic_only,
        (n.fields->'classic'->>'source_id')||':'||(n.fields->'classic'->>'assignee_id') AS classic_subject
    OFFSET 0
) aref ON true
LEFT JOIN principals native_person ON native_person.tenant_id=n.tenant_id
    AND native_person.id=CASE WHEN aref.native ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN aref.native::uuid END
LEFT JOIN identities classic_identity ON aref.classic_only
    AND classic_identity.issuer='paimos-classic' AND classic_identity.subject=aref.classic_subject
LEFT JOIN principals classic_person ON classic_person.tenant_id=n.tenant_id
    AND classic_person.identity_id=classic_identity.id
LEFT JOIN principals assignee_target ON assignee_target.tenant_id=n.tenant_id
    AND assignee_target.id=coalesce(native_person.linked_to,classic_person.linked_to)
LEFT JOIN LATERAL (SELECT coalesce(assignee_target.id,native_person.id,classic_person.id) AS id,
    coalesce(assignee_target.name,native_person.name,classic_person.name) AS name) assignee ON true `

// Label and tag expressions over a node n. Native fields win, also an
// explicit null; imported work falls back to its classic copy.
func labelSQL(field string) string {
	value := func(path string) string {
		return `CASE jsonb_typeof(` + path + `) WHEN 'object' THEN coalesce(` + path + `->>'label',` + path + `->>'name') WHEN 'string' THEN ` + path + `#>>'{}' END`
	}
	return `btrim(coalesce(CASE WHEN n.fields ? '` + field + `' THEN ` + value(`(n.fields->'`+field+`')`) + ` ELSE ` + value(`(n.fields->'classic'->'`+field+`')`) + ` END,''))`
}

// tagElems lists a node's tag names (string or {"name": ...} elements).
const tagElems = `(SELECT btrim(CASE jsonb_typeof(t) WHEN 'string' THEN t#>>'{}' ELSE t->>'name' END) AS name
    FROM jsonb_array_elements(CASE WHEN jsonb_typeof(n.fields->'tags')='array' THEN n.fields->'tags' ELSE '[]'::jsonb END) t)`

// lower-cased tag names of n, without blanks.
const tagNamesSQL = `(SELECT coalesce(array_agg(lower(e.name)),ARRAY[]::text[]) FROM ` + tagElems + ` e WHERE coalesce(e.name,'')<>'')`

// SQL compiles the ordinary work predicates for node lists and delivery reads.
// Execute its CTE in a tenant transaction with the caller's current principal.
func SQL(q Query, sortFields bool) (string, []any) {
	// One tenant transaction supplies RLS to every table in the CTE.
	array := func(values []string) []string {
		if values == nil {
			return []string{}
		}
		return values
	}
	args := []any{q.KindID, array(q.Kinds), array(q.States), array(q.Priorities), array(q.Assignees), q.Q, q.Within, q.ParentSet, q.ParentID, q.Descendants, q.HideClosed}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	// Optional filters join the query only when set, so an unfiltered list
	// plans exactly as before.
	conditions := ""
	add := func(values []string, match func([]string) string) {
		if len(values) > 0 {
			conditions += "\n        AND " + match(values)
		}
	}
	not := func(match func([]string) string) func([]string) string {
		return func(values []string) string { return "NOT (" + match(values) + ")" }
	}
	add(q.KindsNot, func(v []string) string {
		return `NOT (k.slug=ANY(` + arg(v) + `::text[]) OR n.kind_id::text=ANY(` + arg(v) + `::text[]))`
	})
	add(q.IDs, func(v []string) string { return `n.id=ANY(` + arg(v) + `::uuid[])` })
	add(q.StatesNot, func(v []string) string { return `NOT (n.state=ANY(` + arg(v) + `::text[]))` })
	add(q.PrioritiesNot, func(v []string) string {
		return `NOT (coalesce(nullif(n.fields->>'priority',''),'none')=ANY(` + arg(v) + `::text[]))`
	})
	add(q.AssigneesNot, func(v []string) string {
		p := arg(v)
		return `NOT (coalesce(assignee.id::text,'none')=ANY(` + p + `::text[])
            OR coalesce(assignee.id IN (SELECT coalesce(linked_to,id) FROM principals WHERE id::text=ANY(` + p + `::text[])),false))`
	})
	tagMatch := func(values []string) string {
		p := arg(values)
		return `(SELECT names && ` + p + `::text[] OR ('none'=ANY(` + p + `::text[]) AND cardinality(names)=0) FROM (SELECT ` + tagNamesSQL + ` AS names) tn)`
	}
	add(q.Tags, tagMatch)
	add(q.TagsNot, not(tagMatch))
	labelMatch := func(field string) func([]string) string {
		return func(values []string) string {
			return `coalesce(nullif(lower(` + labelSQL(field) + `),''),'none')=ANY(` + arg(values) + `::text[])`
		}
	}
	add(q.CostUnits, labelMatch("cost_unit"))
	add(q.CostUnitsNot, not(labelMatch("cost_unit")))
	add(q.Releases, labelMatch("release"))
	add(q.ReleasesNot, not(labelMatch("release")))
	shipsMatch := func(values []string) string {
		return `coalesce(place.release_node_id::text,'none')=ANY(` + arg(values) + `::text[])`
	}
	if len(q.ShipsIn)+len(q.ShipsInNot) > 0 {
		// No effective placement in journey mode: it must not look like backlog.
		conditions += "\n        AND place.project_node_id IS NOT NULL"
		add(q.ShipsIn, shipsMatch)
		add(q.ShipsInNot, not(shipsMatch))
	}
	checkMatch := func(values []string) string {
		return `(CASE WHEN ` + pendingHumanCheckSQL + ` THEN 'pending' ELSE 'none' END)=ANY(` + arg(values) + `::text[])`
	}
	add(q.HumanChecks, checkMatch)
	add(q.HumanChecksNot, not(checkMatch))
	// Epic subtrees: members of the named epics, or of every epic when "none"
	// is asked for. The walk only runs when an epic filter is set.
	epicCTE := ""
	if len(q.Epics)+len(q.EpicsNot) > 0 {
		ids, all := []string{}, false
		for _, v := range append(append([]string{}, q.Epics...), q.EpicsNot...) {
			if v == "none" {
				all = true
			} else {
				ids = append(ids, v)
			}
		}
		epicCTE = `, epic_members(id, epic_id) AS (
        SELECT c.id, e.id FROM node_kinds ek CROSS JOIN LATERAL (
            SELECT id,tenant_id FROM nodes WHERE tenant_id=ek.tenant_id AND kind_id=ek.id AND deleted_at IS NULL OFFSET 0
        ) e
        CROSS JOIN LATERAL (SELECT id FROM nodes WHERE tenant_id=e.tenant_id AND parent_id=e.id AND deleted_at IS NULL OFFSET 0) c
        WHERE ek.tenant_id=current_setting('aeon.tenant_id')::uuid AND ek.slug='epic'
          AND (` + arg(all) + `::bool OR e.id::text=ANY(` + arg(ids) + `::text[]))
          AND ($7::uuid IS NULL OR e.id IN (SELECT id FROM scope))
        UNION ALL SELECT c.id, m.epic_id FROM epic_members m CROSS JOIN LATERAL (
            SELECT id FROM nodes WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND parent_id=m.id AND deleted_at IS NULL OFFSET 0
        ) c
    )`
		epicMatch := func(values []string) string {
			p := arg(values)
			return `(n.id IN (SELECT id FROM epic_members WHERE epic_id::text=ANY(` + p + `::text[]))
            OR ('none'=ANY(` + p + `::text[]) AND k.slug<>'epic' AND n.id NOT IN (SELECT id FROM epic_members)))`
		}
		add(q.Epics, epicMatch)
		add(q.EpicsNot, not(epicMatch))
	}
	if q.DateField != "" {
		var from, to any
		if q.DateFrom != nil {
			from = *q.DateFrom
		}
		if q.DateTo != nil {
			to = *q.DateTo
		}
		switch q.DateField {
		case "created", "updated":
			column := "n." + q.DateField + "_at"
			conditions += "\n        AND " + column + ">=coalesce(" + arg(from) + "::timestamptz,'-infinity') AND " + column + "<coalesce(" + arg(to) + "::timestamptz,'infinity')"
		default:
			day := func(t *time.Time) any {
				if t == nil {
					return nil
				}
				return t.Format("2006-01-02")
			}
			value := `aeon_field_date(n.fields->>'` + dateFieldKeys[q.DateField] + `')`
			conditions += "\n        AND " + value + " IS NOT NULL AND " + value + ">=coalesce(" + arg(day(q.DateFrom)) + "::date,'-infinity') AND " + value + "<coalesce(" + arg(day(q.DateTo)) + "::date,'infinity')"
		}
	}
	from, scopeCondition := "nodes n", ""
	kindJoin := ` JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id `
	scopeRoot := `coalesce($7::uuid, CASE WHEN $8::bool AND $10::bool THEN $9::uuid ELSE NULL::uuid END)`
	scopeSeed := ""
	if q.Within != nil || (q.ParentSet && q.Descendants) {
		if q.Within != nil {
			scopeSeed = ` AND id=$7::uuid`
		} else {
			scopeSeed = ` AND id=$9::uuid`
		}
		// Drive scoped lists from the tree. A membership subquery can rescan
		// every scope ID for every candidate when the bulk-import statistics
		// change, while a lateral ID lookup stays bounded per tree row.
		from = `scope s CROSS JOIN LATERAL (
            SELECT * FROM nodes WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND id=s.id OFFSET 0
        ) n`
		// Keep kind lookup dependent on the node. Stale tenant statistics on
		// node_kinds can otherwise put kinds before scope and repeat the entire
		// scoped node lookup and its JSON filters once per kind (AEON-248).
		kindJoin = ` JOIN LATERAL (
            SELECT slug FROM node_kinds WHERE id=n.kind_id AND tenant_id=n.tenant_id OFFSET 0
        ) k ON true `
		if q.Within != nil {
			scopeCondition = ` AND n.id<>$7::uuid`
		} else {
			scopeCondition = ` AND n.id<>$9::uuid`
		}
	} else if q.ParentSet {
		scopeCondition = ` AND n.parent_id IS NOT DISTINCT FROM $9::uuid`
	}
	projection := ""
	if sortFields {
		projection = `n.key,n.title,n.body,n.position,n.created_at,n.updated_at,
            coalesce(n.fields->>'priority','') AS priority_raw,`
	}
	deliveryJoin, deliveryProjection := "", "NULL::jsonb AS delivery_order,"
	if wantsDelivery(q) {
		deliveryJoin = ` LEFT JOIN ` + deliverymodel.Effective + ` place ON place.tenant_id=n.tenant_id AND place.project_node_id=n.project_id AND place.item_node_id=n.id `
		deliveryProjection = `CASE WHEN place.project_node_id IS NOT NULL THEN jsonb_build_object('release_id',place.release_node_id,'release_rank',place.release_rank,'rank',place.rank,'expedite',place.expedite) END AS delivery_order,`
		if sortFields {
			deliveryProjection += `place.project_node_id AS delivery_project_id,place.expedite AS delivery_expedite,place.release_rank AS delivery_release_rank,place.rank AS delivery_rank,`
		}
	}
	projection += deliveryProjection
	assigneeSQL, assigneeID := AssigneeJoin, "assignee.id::text"
	assigneePredicate := `AND (cardinality($5::text[])=0 OR coalesce(assignee.id::text,'none')=ANY($5::text[])
            OR assignee.id IN (SELECT coalesce(linked_to,id) FROM principals WHERE id::text=ANY($5::text[])))`
	needsAssignee := len(q.Assignees)+len(q.AssigneesNot) > 0 || slices.Contains(q.FacetNames, "assignee") || slices.ContainsFunc(q.Sort, func(key SortKey) bool { return key.Name == "assignee" })
	if !needsAssignee {
		// Page readers resolve selected people's names after selection. Avoid
		// reading large imported fields and identity mappings for unused rows.
		assigneeSQL, assigneeID = "", "NULL::text"
		assigneePredicate = `AND cardinality($5::text[])=0`
	}
	// Hide closed uses the same buckets as the project counts. Until the filter
	// is on, the predicate stays the previous literal so an unfiltered list
	// keeps its plan.
	closedPred := `n.state NOT IN ('done','cancelled','archived','delivered','accepted')`
	configuredCTE := ""
	configuredJoin := ""
	if q.HideClosed {
		configuredCTE = `, ` + workStateCategoryCTE()
		configuredJoin = ` LEFT JOIN configured cfg ON cfg.kind_id=n.kind_id AND cfg.norm=` + workStateNormSQL("n.state")
		closedPred = workNotClosedSQL("n.state", "cfg")
	}
	return `WITH RECURSIVE scope(id) AS (
        SELECT id FROM nodes WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND deleted_at IS NULL AND id=` + scopeRoot + scopeSeed + `
        UNION ALL SELECT c.id FROM scope s CROSS JOIN LATERAL (
            SELECT id FROM nodes WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND parent_id=s.id AND deleted_at IS NULL
            ORDER BY updated_at DESC OFFSET 0
        ) c
    )` + epicCTE + configuredCTE + `, filtered AS MATERIALIZED (
		SELECT n.id,n.project_id,` + projection + `
            ` + assigneeID + ` AS assignee_id,n.state,k.slug AS kind_slug,
            coalesce(nullif(n.fields->>'priority',''),'none') AS priority FROM ` + from + kindJoin + configuredJoin + assigneeSQL + deliveryJoin + `
        WHERE n.deleted_at IS NULL
        AND ($1::uuid IS NULL OR n.kind_id=$1::uuid)
        AND (cardinality($2::text[])=0 OR k.slug=ANY($2::text[]) OR n.kind_id::text=ANY($2::text[]))
        AND (cardinality($3::text[])=0 OR n.state=ANY($3::text[]))
        AND (cardinality($4::text[])=0 OR coalesce(nullif(n.fields->>'priority',''),'none')=ANY($4::text[]))
        ` + assigneePredicate + `
        AND ($6::text='' OR n.key ILIKE '%'||$6::text||'%' OR n.title ILIKE '%'||$6::text||'%' OR n.body ILIKE '%'||$6::text||'%'
            OR EXISTS(SELECT 1 FROM node_key_aliases a WHERE a.tenant_id=n.tenant_id AND a.node_id=n.id
                AND a.key ILIKE '%'||$6::text||'%')
            OR EXISTS(WITH RECURSIVE ancestors AS (
                SELECT a.id,a.parent_id,a.kind_id,a.key,a.title,a.body,1 depth FROM nodes a WHERE a.tenant_id=n.tenant_id AND a.id=n.parent_id AND a.deleted_at IS NULL AND a.project_id=n.project_id
                UNION ALL SELECT a.id,a.parent_id,a.kind_id,a.key,a.title,a.body,up.depth+1 FROM ancestors up JOIN nodes a ON a.tenant_id=n.tenant_id AND a.id=up.parent_id AND a.deleted_at IS NULL AND a.project_id=n.project_id WHERE up.depth<64
            ) SELECT 1 FROM ancestors a JOIN node_kinds ak ON ak.tenant_id=n.tenant_id AND ak.id=a.kind_id WHERE ak.slug='epic' AND (a.key ILIKE '%'||$6::text||'%' OR a.title ILIKE '%'||$6::text||'%' OR a.body ILIKE '%'||$6::text||'%')))
        AND (NOT $11::bool OR ` + closedPred + `)` + scopeCondition + conditions + `
    )`, args
}

const pendingHumanCheckSQL = `(n.human_check IS NOT NULL AND btrim(n.human_check)<>'')`

var dateFieldKeys = map[string]string{"start": "start_date", "end": "end_date", "accepted": "accepted_at"}

func wantsDelivery(q Query) bool {
	for _, key := range q.Sort {
		if key.Name == "order" {
			return true
		}
	}
	return len(q.ShipsIn)+len(q.ShipsInNot) > 0 || slices.Contains(q.FacetNames, "ships_in")
}
