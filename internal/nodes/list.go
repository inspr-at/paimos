// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/tenant"
)

type listPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// HasAvatar says a picture exists, so clients request
	// /api/people/{id}/avatar/{size} only then and show initials otherwise.
	HasAvatar bool `json:"has_avatar"`
}
type listParent struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Title    string `json:"title"`
	KindSlug string `json:"kind_slug"`
}
type listProject struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Title string `json:"title"`
}

// listEpic is the nearest epic above an item: a ticket's own epic, or for a
// task the epic of its ticket. Nil when the item sits under no epic.
type listEpic struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Title string `json:"title"`
}
type listItem struct {
	nodeJSON
	KindSlug      string       `json:"kind_slug"`
	KindLabel     string       `json:"kind_label"`
	Priority      *string      `json:"priority"`
	Assignee      *listPerson  `json:"assignee"`
	Parent        *listParent  `json:"parent"`
	ChildrenCount int          `json:"children_count"`
	Project       *listProject `json:"project"`
	Epic          *listEpic    `json:"epic"`
	Eta           *eta.View    `json:"eta,omitempty"`
	// LeadWorker is the live session the Assignee cell leads with. Name is the
	// permission-projected label. Key identifies that session for the client
	// without carrying a session id the caller cannot already see.
	LeadWorker *leadWorker `json:"lead_worker,omitempty"`
	// Planning is the resolved model, tokens and (with harness.read) cost of a
	// ticket, task or epic (AEON-329). Absent when there is nothing to show.
	Planning *planningView `json:"planning,omitempty"`
}

// leadWorker is one bound live session, chosen by the server.
type leadWorker struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}
type nodePage struct {
	Items      []listItem                `json:"items"`
	NextCursor *string                   `json:"next_cursor"`
	Facets     map[string]map[string]int `json:"facets,omitempty"`
}
type treeEntry struct {
	Node  nodeJSON `json:"node"`
	Depth int      `json:"depth"`
}
type treePage struct {
	Items      []treeEntry `json:"items"`
	NextCursor *string     `json:"next_cursor"`
}
type sortKey struct {
	Name string `json:"name"`
	Desc bool   `json:"desc"`
}
type listQuery struct {
	KindID      *string   `json:"kind_id"`
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
	Sort        []sortKey `json:"sort"`
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
	Tags         []string `json:"tags,omitempty"`
	TagsNot      []string `json:"tags_not,omitempty"`
	CostUnits    []string `json:"cost_units,omitempty"`
	CostUnitsNot []string `json:"cost_units_not,omitempty"`
	Releases     []string `json:"releases,omitempty"`
	ReleasesNot  []string `json:"releases_not,omitempty"`
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
	// seen and the lead thresholds are filled by listNodes. They are not
	// request input and stay out of the cursor fingerprint (unexported).
	seen       assigneeSeen
	leadYellow int
	leadRed    int
	planRates  json.RawMessage
}
type listCursor struct {
	Hash string `json:"hash"`
	ID   string `json:"id"`
}
type treeQuery struct {
	RootID   *string
	DepthSet bool
	MaxDepth int
	Limit    int
	Cursor   string
}

var validSort = map[string]bool{"key": true, "title": true, "state": true, "priority": true, "kind": true, "updated_at": true, "created_at": true, "position": true, "assignee": true, "eta_ready": true, "progress": true, "estimate": true, "model": true, "tokens": true, "list_cost": true, "paid": true}
var validFacet = map[string]bool{"state": true, "kind": true, "priority": true, "assignee": true, "tag": true, "cost_unit": true, "release": true}

// dateFieldKeys maps date_field to the fields key of dates kept in node fields.
var dateFieldKeys = map[string]string{"start": "start_date", "end": "end_date", "accepted": "accepted_at"}
var slugOrID = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (m *Module) handleListNodes(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	q, err := parseListQuery(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	page, err := m.listNodes(r.Context(), p.TenantID, q)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func (m *Module) handleTree(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	q, err := parseTreeQuery(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	page, err := m.nodeTree(r.Context(), p.TenantID, q)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func commaValues(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			v := strings.TrimSpace(part)
			if !seen[v] {
				out = append(out, v)
				seen[v] = true
			}
		}
	}
	return out
}
func queryList(r *http.Request, name string) ([]string, error) {
	q := r.URL.Query()
	if !q.Has(name) {
		return nil, nil
	}
	values := commaValues(q[name])
	for _, v := range values {
		if v == "" {
			return nil, badRequest("invalid " + name)
		}
	}
	return values, nil
}
func queryBool(r *http.Request, name string) (bool, error) {
	if !r.URL.Query().Has(name) {
		return false, nil
	}
	switch r.URL.Query().Get(name) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, badRequest("invalid " + name)
	}
}
func parseListQuery(r *http.Request) (listQuery, error) {
	v := r.URL.Query()
	out := listQuery{Limit: 50}
	if v.Has("kind_id") {
		id, ok := parseUUID(v.Get("kind_id"))
		if !ok {
			return out, badRequest("invalid kind_id")
		}
		out.KindID = &id
	}
	var err error
	if out.Kinds, err = queryList(r, "kind"); err != nil {
		return out, err
	}
	for _, kind := range out.Kinds {
		if _, ok := parseUUID(kind); !ok && !slugOrID.MatchString(kind) {
			return out, badRequest("invalid kind")
		}
	}
	if out.States, out.StatesNot, err = negatedList(r, "state", nil); err != nil {
		return out, err
	}
	if out.Priorities, out.PrioritiesNot, err = negatedList(r, "priority", nil); err != nil {
		return out, err
	}
	personOrNone := func(v string) (string, bool) {
		if v == "none" {
			return v, true
		}
		return parseUUID(v)
	}
	if out.Assignees, out.AssigneesNot, err = negatedList(r, "assignee", personOrNone); err != nil {
		return out, err
	}
	label := func(v string) (string, bool) {
		v = strings.ToLower(strings.TrimSpace(v))
		return v, v != "" && len(v) <= 200
	}
	if out.Tags, out.TagsNot, err = negatedList(r, "tag", label); err != nil {
		return out, err
	}
	if out.CostUnits, out.CostUnitsNot, err = negatedList(r, "cost_unit", label); err != nil {
		return out, err
	}
	if out.Releases, out.ReleasesNot, err = negatedList(r, "release", label); err != nil {
		return out, err
	}
	if out.Epics, out.EpicsNot, err = negatedList(r, "epic", personOrNone); err != nil {
		return out, err
	}
	if err = parseDateFilter(r, &out); err != nil {
		return out, err
	}
	if out.FacetNames, err = queryList(r, "facets"); err != nil {
		return out, err
	}
	for _, f := range out.FacetNames {
		if !validFacet[f] {
			return out, badRequest("invalid facets")
		}
	}
	out.Q = strings.TrimSpace(v.Get("q"))
	if v.Has("within") {
		id, ok := parseUUID(v.Get("within"))
		if !ok {
			return out, badRequest("invalid within")
		}
		out.Within = &id
	}
	if v.Has("parent_id") {
		id, ok := parseUUID(v.Get("parent_id"))
		if !ok {
			return out, badRequest("invalid parent_id")
		}
		out.ParentSet = true
		out.ParentID = &id
	}
	if out.Descendants, err = queryBool(r, "include_descendants"); err != nil {
		return out, err
	}
	if out.HideClosed, err = queryBool(r, "hide_closed"); err != nil {
		return out, err
	}
	if out.Descendants && !out.ParentSet && out.Within == nil {
		return out, badRequest("include_descendants requires parent_id")
	}
	if out.Within != nil && (out.ParentSet || v.Has("include_descendants")) {
		return out, badRequest("within conflicts with parent_id and include_descendants")
	}
	direction := "asc"
	if v.Has("direction") {
		direction = v.Get("direction")
		if direction != "asc" && direction != "desc" {
			return out, badRequest("invalid direction")
		}
	}
	sortRaw := "position"
	if v.Has("sort") {
		sortRaw = v.Get("sort")
	}
	for _, s := range strings.Split(sortRaw, ",") {
		s = strings.TrimSpace(s)
		desc := direction == "desc"
		if strings.HasPrefix(s, "-") {
			s = strings.TrimPrefix(s, "-")
			desc = true
		}
		if !validSort[s] {
			return out, badRequest("invalid sort")
		}
		out.Sort = append(out.Sort, sortKey{s, desc})
	}
	out.Limit, err = parseLimit(v.Get("limit"), v.Has("limit"))
	if err != nil {
		return out, err
	}
	out.Cursor = v.Get("cursor")
	return out, nil
}

// negatedList reads a list parameter whose values may carry a leading "!"
// (excluded). check normalises and validates each value (nil keeps it).
func negatedList(r *http.Request, name string, check func(string) (string, bool)) (in, out []string, err error) {
	values, err := queryList(r, name)
	if err != nil || values == nil {
		return nil, nil, err
	}
	for _, v := range values {
		negated := strings.HasPrefix(v, "!")
		v = strings.TrimPrefix(v, "!")
		if check != nil {
			var ok bool
			if v, ok = check(v); !ok {
				return nil, nil, badRequest("invalid " + name)
			}
		}
		if v == "" {
			return nil, nil, badRequest("invalid " + name)
		}
		if negated {
			out = append(out, v)
		} else {
			in = append(in, v)
		}
	}
	return in, out, nil
}

func parseDateFilter(r *http.Request, out *listQuery) error {
	v := r.URL.Query()
	field := v.Get("date_field")
	if field == "" {
		if v.Has("date_from") || v.Has("date_to") {
			return badRequest("date_from and date_to need date_field")
		}
		return nil
	}
	switch field {
	case "created", "updated", "start", "end", "accepted":
	default:
		return badRequest("invalid date_field")
	}
	out.DateField = field
	for name, dst := range map[string]**time.Time{"date_from": &out.DateFrom, "date_to": &out.DateTo} {
		if !v.Has(name) {
			continue
		}
		at, err := time.Parse(time.RFC3339, v.Get(name))
		if err != nil {
			return badRequest("invalid " + name)
		}
		*dst = &at
	}
	if out.DateFrom != nil && out.DateTo != nil && !out.DateTo.After(*out.DateFrom) {
		return badRequest("date_to must be after date_from")
	}
	return nil
}

func listFingerprint(q listQuery) string {
	q.Cursor = ""
	raw, _ := json.Marshal(q)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (m *Module) listNodes(ctx context.Context, tenantID string, q listQuery) (nodePage, error) {
	var mark *listCursor
	if q.Cursor != "" {
		env, err := openCursor(q.Cursor)
		if err != nil {
			return nodePage{}, err
		}
		if env.T != "list" {
			return nodePage{}, badRequest("cursor does not match this query")
		}
		var c listCursor
		if json.Unmarshal(env.P, &c) != nil || c.Hash != listFingerprint(q) {
			return nodePage{}, badRequest("cursor does not match this query")
		}
		if _, ok := parseUUID(c.ID); !ok {
			return nodePage{}, badRequest("invalid cursor")
		}
		mark = &c
	}
	page := nodePage{Items: []listItem{}}
	err := m.tx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var anchor any
		if mark != nil {
			anchor = mark.ID
		}
		// The lead is decided in this one statement. An assignee sort probes a
		// live lead while ordering only when the row has no stored person, and
		// reads the page's remaining leads from that same statement.
		// Thresholds are the viewer's normalized preference.
		q.leadYellow, q.leadRed = 3, 10
		if p, ok := tenant.PrincipalFrom(ctx); ok {
			seen, err := assigneeAudience(ctx, tx, p)
			if err != nil {
				return dbErr("assignee audience", err)
			}
			q.seen = seen
			yellow, red, err := readLeadMinutes(ctx, tx, p.ID)
			if err != nil {
				return dbErr("lead thresholds", err)
			}
			q.leadYellow, q.leadRed = yellow, red
		}
		if sortsByPlanningValue(q) {
			if err := preparePlanningSort(ctx, tx, &q); err != nil {
				return dbErr("planning sort", err)
			}
			// Nested loops from the filtered tickets into usage become a
			// per-row visibility probe once that table holds other tenants.
			// Hash the usage read instead; later statements want nested loops.
			if _, err := tx.Exec(ctx, `SET LOCAL enable_nestloop = off`); err != nil {
				return dbErr("planning sort", err)
			}
		}
		sql, args := listSQL(q, anchor)
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return dbErr("list nodes", err)
		}
		var money map[string]planMicros
		if sortsByPlanningValue(q) {
			money = map[string]planMicros{}
		}
		for rows.Next() {
			var item listItem
			var fields, position string
			var assigneeID, assigneeName, parentID, parentKey, parentTitle, parentKind, projectID, projectKey, projectTitle, epicID, epicKey, epicTitle *string
			var assigneeAvatar bool
			var leadName, leadKey *string
			var listSpent, listEst, paidSpent, paidEst *string
			dest := []any{&item.ID, &item.Key, &item.KindID, &item.Title, &item.Body, &fields, &item.State, &item.ParentID, &position, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt, &item.KindSlug, &item.KindLabel, &item.Priority, &assigneeID, &assigneeName, &assigneeAvatar, &parentID, &parentKey, &parentTitle, &parentKind, &item.ChildrenCount, &projectID, &projectKey, &projectTitle, &epicID, &epicKey, &epicTitle, &leadName, &leadKey}
			if money != nil {
				dest = append(dest, &listSpent, &listEst, &paidSpent, &paidEst)
			}
			err = rows.Scan(dest...)
			if err != nil {
				rows.Close()
				return err
			}
			if money != nil {
				parsed, err := planMicrosFromText(listSpent, listEst, paidSpent, paidEst)
				if err != nil {
					rows.Close()
					return err
				}
				money[item.ID] = parsed
			}
			if leadName != nil && leadKey != nil {
				item.LeadWorker = &leadWorker{Name: *leadName, Key: *leadKey}
			}
			item.Fields = json.RawMessage(fields)
			item.Position = trimDecimal(position)
			if assigneeID != nil {
				item.Assignee = &listPerson{*assigneeID, *assigneeName, assigneeAvatar}
			}
			if parentID != nil {
				item.Parent = &listParent{*parentID, *parentKey, *parentTitle, *parentKind}
			}
			if projectID != nil {
				item.Project = &listProject{*projectID, *projectKey, *projectTitle}
			}
			if epicID != nil {
				item.Epic = &listEpic{*epicID, *epicKey, *epicTitle}
			}
			page.Items = append(page.Items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if sortsByPlanningValue(q) {
			if _, err := tx.Exec(ctx, `SET LOCAL enable_nestloop = on`); err != nil {
				return err
			}
		}
		if len(page.Items) > q.Limit {
			last := page.Items[q.Limit-1]
			page.Items = page.Items[:q.Limit]
			cursor, err := encodeTyped("list", listCursor{listFingerprint(q), last.ID})
			if err != nil {
				return err
			}
			page.NextCursor = &cursor
		}
		if len(q.FacetNames) > 0 {
			page.Facets = map[string]map[string]int{}
			for _, f := range q.FacetNames {
				page.Facets[f] = map[string]int{}
			}
			sql, args = facetSQL(q)
			rows, err = tx.Query(ctx, sql, args...)
			if err != nil {
				return dbErr("list facets", err)
			}
			for rows.Next() {
				var name, value string
				var count int
				if err = rows.Scan(&name, &value, &count); err != nil {
					rows.Close()
					return err
				}
				page.Facets[name][value] = count
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		if len(page.Items) > 0 {
			ids := make([]string, len(page.Items))
			for i := range page.Items {
				ids[i] = page.Items[i].ID
			}
			estimates, err := loadEstimates(ctx, tx, ids)
			if err != nil {
				return err
			}
			for i := range page.Items {
				page.Items[i].Estimate = estimates[page.Items[i].ID]
			}
			planning, err := loadPlanning(ctx, tx, page.Items, q.seen, money)
			if err != nil {
				return dbErr("list planning", err)
			}
			for i := range page.Items {
				page.Items[i].Planning = planning[page.Items[i].ID]
			}
			views, err := eta.Load(ctx, tx, ids)
			if err != nil {
				return err
			}
			for i := range page.Items {
				if view, ok := views[page.Items[i].ID]; ok {
					copied := view
					page.Items[i].Eta = &copied
				}
			}
		}
		return nil
	})
	return page, err
}

// Native fields win, including an explicit null (unassignment). Classic IDs
// are resolved only through this tenant's source-qualified identity mapping.
// The references are read from n.fields once per node (the OFFSET 0 keeps that
// subquery from being inlined): imported fields are large and stored out of
// line, and comparing them against every principal re-read them each time,
// about 0.4 ms per row on production data (AEON-140).
// hasAvatar is true when the principal has a profile picture; only people
// have one. It reads the page's few assignees after paging, never the filter.
func hasAvatar(principal string) string {
	return `EXISTS (SELECT 1 FROM personal_profiles avatar WHERE avatar.tenant_id=current_setting('aeon.tenant_id')::uuid AND avatar.principal_id=` + principal + ` AND avatar.avatar_hashes <> '{}'::jsonb)`
}

const assigneeJoin = ` LEFT JOIN LATERAL (
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

// assigneeShownSQL is the name the Assignee cell leads with: the stored person,
// otherwise the lead live worker (worker.shown_name).
const assigneeShownSQL = `coalesce(ap.name, worker.shown_name)`

// assigneeSeen is what GET /api/harness-sessions/live would put in who():
// a session label only with harness.read on the ticket's project, a principal
// name with harness.read or workspace members.read, otherwise the harness
// label ("Claude agent"). A zero value withholds both, so a sort cannot
// order by a name the caller is not shown.
type assigneeSeen struct {
	harnessAll bool
	projects   []string
	members    bool
}

// assigneeAudience reads the caller's grants once. Workspace harness.read
// covers every project; a project grant is listed so the sort can match
// that ticket's project_id. members.read is workspace-only.
func assigneeAudience(ctx context.Context, tx pgx.Tx, p tenant.Principal) (assigneeSeen, error) {
	allowed, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return assigneeSeen{}, err
	}
	seen := assigneeSeen{harnessAll: allowed("harness.read", ""), members: allowed("members.read", "")}
	if seen.harnessAll {
		return seen, nil
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.deleted_at IS NULL AND k.slug='project'`)
	if err != nil {
		return assigneeSeen{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return assigneeSeen{}, err
		}
		if allowed("harness.read", id) {
			seen.projects = append(seen.projects, id)
		}
	}
	return seen, rows.Err()
}

// readLeadMinutes is the viewer's agent-state preference, normalized the same
// way as the web: yellow 1–1439 (default 3), red strictly above yellow and at
// most 1440 (default at least 10). A missing row uses 3 and 10.
func readLeadMinutes(ctx context.Context, tx pgx.Tx, principalID string) (int, int, error) {
	if _, ok := parseUUID(principalID); !ok {
		return 3, 10, nil
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT value FROM user_preferences WHERE principal_id=$1::uuid AND key='agent-state'`, principalID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 3, 10, nil
	}
	if err != nil {
		return 0, 0, err
	}
	yellow, red := leadMinutesFromJSON(raw)
	return yellow, red, nil
}

// leadMinutesFromJSON applies normalizeAgentState's minute rules. Non-numbers
// take the fallback; a present red below yellow+1 is lifted, and a missing
// red starts at max(10, yellow+1).
func leadMinutesFromJSON(raw []byte) (int, int) {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return 3, 10
	}
	yellow := boundMinutes(v["yellowMinutes"], 3, 1, 1439)
	red := boundMinutes(v["redMinutes"], max(10, yellow+1), yellow+1, 1440)
	return yellow, red
}

func boundMinutes(n any, fallback, min, maxV int) int {
	f, ok := asFloat(n)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return fallback
	}
	rounded := int(math.Round(f))
	if rounded < min {
		return min
	}
	if rounded > maxV {
		return maxV
	}
	return rounded
}

func asFloat(n any) (float64, bool) {
	switch v := n.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func normalizeLeadMinutes(yellow, red int) (int, int) {
	if yellow < 1 || red < 1 {
		return 3, 10
	}
	if yellow > 1439 {
		yellow = 1439
	}
	if red <= yellow {
		red = yellow + 1
	}
	if red > 1440 {
		red = 1440
	}
	return yellow, red
}

// assigneeLeadKeyExpr matches the web leadWorkerKey. Restricted viewers get
// only immutable public facts: harness and start. Both sides canonicalize the
// live feed's RFC3339Nano timestamp to UTC, trimming fractional zeros. Sessions
// indistinguishable to that viewer can share a key; names and telemetry cannot
// change it or disclose a withheld identity.
func assigneeLeadKeyExpr(harnessAll string) string {
	return `CASE WHEN ` + harnessAll + ` THEN 's:' || s.id::text
        ELSE 'v' || chr(1) || s.harness || chr(1) ||
            rtrim(rtrim(to_char(s.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US'), '0'), '.') || 'Z'
    END`
}

// assigneeLeadOrder is the server's only lead choice. Rank matches the web
// STATE_PRIORITY at this viewer's normalized thresholds (yellow awaiting,
// red unresponsive): problem, unresponsive, waiting, awaiting, throttled,
// working, idle, stale. Waiting is a yielded phase, a waiting run, a stale
// estimate, or a pending approval on this session's run. Ties break by a
// worker before a coordinator, then start, then heartbeat, then public
// session facts, the projected name and the stable viewer-visible key. A
// withheld label or session id must not decide which name is shown or sorted.
func assigneeLeadOrder(yellow, red int, shown, key string) string {
	if yellow == 0 && red == 0 {
		yellow, red = 3, 10
	}
	yellow, red = normalizeLeadMinutes(yellow, red)
	return fmt.Sprintf(`CASE
    WHEN coalesce(run.status IN ('failed','ownership_lost','blocked'), false)
      OR coalesce(s.stop_reason <> 'heartbeat_lost' AND replace(replace(s.stop_reason, '_', ' '), '-', ' ') ~* '\m(error|errored|failed|failure|blocked|crash(ed)?|ownership lost|heartbeat lost|timeout|timed out)\M', false) THEN 0
    WHEN s.phase IN ('starting','working','stopping') AND s.activity NOT IN ('idle','throttled')
      AND coalesce(s.heartbeat_at, s.created_at) <= now() - make_interval(mins => %d) THEN 1
    WHEN s.phase = 'yielded' OR coalesce(run.status = 'waiting', false)
      OR (s.eta_reported_at IS NOT NULL AND now() - s.eta_reported_at > 2 * aeon_eta_interval())
      OR EXISTS (
        SELECT 1 FROM approval_requests a
        WHERE a.tenant_id = s.tenant_id AND a.agent_principal_id = s.agent_principal_id
          AND a.expires_at > now() AND s.run_id IS NOT NULL
          AND coalesce(a.run_id, CASE WHEN a.resource_kind = 'run' THEN a.resource_id END) = s.run_id
          AND NOT EXISTS (SELECT 1 FROM approval_decisions d WHERE d.tenant_id = a.tenant_id AND d.request_id = a.id)
      ) THEN 2
    WHEN s.activity = 'throttled' THEN 4
    WHEN s.phase IN ('starting','working','stopping') AND s.activity NOT IN ('idle','throttled')
      AND (s.heartbeat_at IS NULL OR s.heartbeat_at <= now() - make_interval(mins => %d)) THEN 3
    WHEN s.phase IN ('starting','working','stopping') AND s.activity NOT IN ('idle','throttled') THEN 5
    WHEN s.heartbeat_at IS NULL OR s.heartbeat_at <= now() - make_interval(mins => %d) THEN 7
    ELSE 6
END, (s.role = 'coordinator'), s.created_at, s.heartbeat_at NULLS LAST, s.harness, s.activity_sequence, s.phase, s.activity, (`+shown+`) COLLATE "C", (`+key+`) COLLATE "C"`, red, yellow, yellow)
}

// assigneeWorkerEligible is who may lead: bound, not stopped, not archived.
const assigneeWorkerEligible = `s.stopped_at IS NULL
      AND s.phase<>'stopped'
      AND position('archived' in lower(coalesce(s.stop_reason, '')))=0`

// assigneeHarnessLabel is who()'s last resort: the harness word plus " agent".
const assigneeHarnessLabel = `CASE s.harness WHEN 'codex' THEN 'Codex' WHEN 'claude' THEN 'Claude' WHEN 'pi' THEN 'Pi'
        WHEN 'cursor' THEN 'Cursor' WHEN 'grok' THEN 'Grok'
        ELSE upper(left(s.harness, 1)) || substr(s.harness, 2) END || ' agent'`

// assigneeShownExpr is the name this caller would see for session s: a
// session label with harness.read on the ticket's project, a principal name
// with harness.read or members.read, otherwise the harness label.
func assigneeShownExpr(projectID, harnessAll, projects, members string) string {
	return `CASE
        WHEN ` + harnessAll + ` OR ` + projectID + ` = ANY(` + projects + `::uuid[])
            THEN coalesce(nullif(btrim(s.display_label), ''), nullif(btrim(agent.name), ''), ` + assigneeHarnessLabel + `)
        WHEN ` + members + `
            THEN coalesce(nullif(btrim(agent.name), ''), ` + assigneeHarnessLabel + `)
        ELSE ` + assigneeHarnessLabel + `
    END`
}

// assigneeWorkerJoin chooses and projects the lead together. gate is an outer
// boolean. Empty means every row; otherwise the probe runs only when the gate
// is true (CASE does not evaluate that branch). Assignee sorts pass
// "ap.name IS NULL" while ordering, because a stored person is already the
// sort key. The same lookup returns lead_evaluated: true when the probe ran,
// including when it found nobody. The page gates on "NOT s.lead_evaluated",
// so an empty result is not probed again and a stored person still receives
// a lead from this statement. Other sorts attach the lookup only to the
// selected page. The partial index harness_sessions_ticket_eta probes one ticket.
//
// gated is MATERIALIZED. Inlining it re-runs the lookup once per reference:
// the sort reads the shown name twice, and the statement also carries the
// name and the key, four probes per unassigned row. The CTE evaluates once.
func assigneeWorkerJoin(nodeID, projectID, harnessAll, projects, members string, yellow, red int, gate string) string {
	shown := assigneeShownExpr(projectID, harnessAll, projects, members)
	key := assigneeLeadKeyExpr(harnessAll)
	lookup := `SELECT ` + shown + ` AS shown_name, ` + key + ` AS lead_key
    FROM harness_sessions s
    LEFT JOIN principals agent ON agent.tenant_id=s.tenant_id AND agent.id=s.agent_principal_id
    LEFT JOIN agent_runs run ON run.tenant_id=s.tenant_id AND run.id=s.run_id
    WHERE s.tenant_id=current_setting('aeon.tenant_id')::uuid
      AND s.ticket_node_id=` + nodeID + `
      AND ` + assigneeWorkerEligible + `
    ORDER BY ` + assigneeLeadOrder(yellow, red, shown, key) + `
    LIMIT 1`
	if gate == "" {
		return ` LEFT JOIN LATERAL (
    ` + lookup + `
) worker ON true `
	}
	// Rename the session so a gate that reads the selected page (alias s)
	// does not bind to this scan.
	scoped := strings.ReplaceAll(lookup, "s.", "sess.")
	scoped = strings.ReplaceAll(scoped, "harness_sessions s", "harness_sessions sess")
	return ` LEFT JOIN LATERAL (
    WITH decision AS (
        SELECT (` + gate + `) AS lead_evaluated
    ), gated AS MATERIALIZED (
        SELECT decision.lead_evaluated,
            CASE WHEN decision.lead_evaluated THEN (
            SELECT jsonb_build_object('shown_name', picked.shown_name, 'lead_key', picked.lead_key)
            FROM (` + scoped + `) picked
        ) END AS payload
        FROM decision
    )
    SELECT payload->>'shown_name' AS shown_name, payload->>'lead_key' AS lead_key, lead_evaluated
    FROM gated
) worker ON true `
}

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

func listFilterSQL(q listQuery, sortFields bool) (string, []any) {
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
            assignee.id::text AS assignee_id,n.state,k.slug AS kind_slug,
            coalesce(nullif(n.fields->>'priority',''),'none') AS priority FROM ` + from + kindJoin + configuredJoin + assigneeJoin + `
        WHERE n.deleted_at IS NULL
        AND ($1::uuid IS NULL OR n.kind_id=$1::uuid)
        AND (cardinality($2::text[])=0 OR k.slug=ANY($2::text[]) OR n.kind_id::text=ANY($2::text[]))
        AND (cardinality($3::text[])=0 OR n.state=ANY($3::text[]))
        AND (cardinality($4::text[])=0 OR coalesce(nullif(n.fields->>'priority',''),'none')=ANY($4::text[]))
        AND (cardinality($5::text[])=0 OR coalesce(assignee.id::text,'none')=ANY($5::text[])
            OR assignee.id IN (SELECT coalesce(linked_to,id) FROM principals WHERE id::text=ANY($5::text[])))
        AND ($6::text='' OR n.key ILIKE '%'||$6::text||'%' OR n.title ILIKE '%'||$6::text||'%' OR n.body ILIKE '%'||$6::text||'%'
            OR EXISTS(SELECT 1 FROM node_key_aliases a WHERE a.tenant_id=n.tenant_id AND a.node_id=n.id
                AND a.key ILIKE '%'||$6::text||'%'))
        AND (NOT $11::bool OR ` + closedPred + `)` + scopeCondition + conditions + `
    )`, args
}
func listOrder(q listQuery) string {
	parts := []string{}
	if q.Q != "" {
		// Key prefixes lead, then title matches, then matches in the description only.
		parts = append(parts, "CASE WHEN f.key ILIKE $6::text||'%' THEN 0 WHEN f.body ILIKE '%'||$6::text||'%' AND f.title NOT ILIKE '%'||$6::text||'%' AND f.key NOT ILIKE '%'||$6::text||'%' THEN 2 ELSE 1 END ASC")
	}
	for _, key := range q.Sort {
		dir := "ASC"
		if key.Desc {
			dir = "DESC"
		}
		switch key.Name {
		case "key":
			parts = append(parts, `regexp_replace(f.key,'-[0-9]+$','') `+dir, `substring(f.key from '-([0-9]+)$')::numeric `+dir)
		case "state":
			parts = append(parts, workStateKnownSQL("f.state")+` ASC`, workStateOrderSQL("f.state")+` `+dir, "f.state "+dir)
		case "priority":
			parts = append(parts, `CASE f.priority WHEN 'high' THEN 0 WHEN 'medium' THEN 1 WHEN 'low' THEN 2 WHEN 'none' THEN 3 ELSE 4 END `+dir, `f.priority_raw `+dir)
		case "kind":
			parts = append(parts, "f.kind_slug "+dir)
		case "assignee":
			// The shown name, empty last in both directions. ap.id then f.id
			// keep people who share a name in a stable order.
			parts = append(parts, assigneeShownSQL+" IS NULL ASC", "lower("+assigneeShownSQL+") "+dir, "ap.id "+dir)
		case "eta_ready":
			parts = append(parts, "eta.eta_ready_at IS NULL ASC", "eta.eta_ready_at "+dir)
		case "estimate":
			parts = append(parts, "est.hours IS NULL ASC", "est.hours "+dir)
		case "progress":
			parts = append(parts, "eta.progress_pct IS NULL ASC", "eta.progress_pct "+dir)
		case "model":
			// The role's rung on the ladder, then the area; rows without a role last.
			parts = append(parts, "route.rank IS NULL ASC", "route.rank "+dir, "route.area IS NULL ASC", "route.area "+dir)
		case "tokens", "list_cost", "paid":
			// The same integer the cell prints: spent micro-dollars, else the
			// estimate. Rounded once in the CTE as numeric, then the id tiebreaker.
			// The amount is a CTE projection, so an expression index cannot
			// serve it, and rounding inside the usage aggregate does not shrink
			// the plan: sorting those rows is noise next to building it.
			value := map[string]string{
				"tokens":    "plan.tokens",
				"list_cost": "plan.list_micros",
				"paid":      "plan.paid_micros",
			}[key.Name]
			parts = append(parts, value+" IS NULL ASC", value+" "+dir)
		default:
			parts = append(parts, "f."+key.Name+" "+dir)
		}
	}
	return strings.Join(append(parts, "f.id ASC"), ",")
}
func sortsBy(q listQuery, name string) bool {
	for _, key := range q.Sort {
		if key.Name == name {
			return true
		}
	}
	return false
}
func listSQL(q listQuery, anchor any) (string, []any) {
	prefix, args := listFilterSQL(q, true)
	args = append(args, anchor, q.Limit+1)
	anchorArg, limitArg := fmt.Sprintf("$%d", len(args)-1), fmt.Sprintf("$%d", len(args))
	projects := q.seen.projects
	if projects == nil {
		projects = []string{}
	}
	args = append(args, q.seen.harnessAll, projects, q.seen.members)
	harnessAll, projectArg, members := fmt.Sprintf("$%d", len(args)-2), fmt.Sprintf("$%d", len(args)-1), fmt.Sprintf("$%d", len(args))
	workerJoin := func(nodeID, projectID, gate string) string {
		return assigneeWorkerJoin(nodeID, projectID, harnessAll, projectArg, members, q.leadYellow, q.leadRed, gate)
	}
	people, orderedLead := "", ""
	leadColumns := "worker.shown_name,worker.lead_key"
	pageWorker := workerJoin("n.id", "n.project_id", "")
	if sortsBy(q, "assignee") {
		// A stored person is the sort key, so only the other rows need a live
		// probe before paging. lead_evaluated stays true when that probe finds
		// nobody; the page probes only the rows this pass deferred.
		people = ` LEFT JOIN principals ap ON ap.tenant_id=current_setting('aeon.tenant_id')::uuid AND ap.id=f.assignee_id::uuid` + workerJoin("f.id", "f.project_id", "ap.name IS NULL")
		orderedLead = ",worker.shown_name AS lead_name,worker.lead_key,worker.lead_evaluated"
		leadColumns = "coalesce(s.lead_name,worker.shown_name),coalesce(s.lead_key,worker.lead_key)"
		pageWorker = workerJoin("n.id", "n.project_id", "NOT s.lead_evaluated")
	}
	etaJoin := ""
	if sortsBy(q, "eta_ready") || sortsBy(q, "progress") {
		etaJoin = ` LEFT JOIN LATERAL aeon_node_eta(f.id) eta ON true`
	}
	estimateJoin, planningCTE, planningJoin := "", "", ""
	if sortsBy(q, "model") {
		planningJoin = ` LEFT JOIN LATERAL (
            SELECT CASE rn.fields->>'route_role' WHEN 'scout' THEN 0 WHEN 'mechanical' THEN 1 WHEN 'build' THEN 2 WHEN 'build-hard' THEN 3 WHEN 'review-gate' THEN 4 END AS rank,
                nullif(rn.fields->>'area','') AS area
            FROM nodes rn WHERE rn.tenant_id=current_setting('aeon.tenant_id')::uuid AND rn.id=f.id
        ) route ON true`
	}
	if sortsByPlanningValue(q) {
		rates := q.planRates
		if len(rates) == 0 {
			rates = json.RawMessage(`[]`)
		}
		args = append(args, string(rates))
		planningCTE = planningSortSQL(fmt.Sprintf("$%d", len(args)), harnessAll, projectArg, false)
		planningJoin += ` LEFT JOIN planning_values plan ON plan.id=f.id`
	}
	moneyJoin, moneyCols := "", ""
	if sortsByPlanningValue(q) {
		moneyJoin = ` LEFT JOIN planning_values pm ON pm.id=n.id`
		moneyCols = `, ` + usdMicrosTextSQL("pm.list_spent_micros") + `, ` + usdMicrosTextSQL("pm.list_est_micros") + `, ` + usdMicrosTextSQL("pm.paid_spent_micros") + `, ` + usdMicrosTextSQL("pm.paid_est_micros")
	}
	if sortsBy(q, "estimate") {
		estimateJoin = ` LEFT JOIN LATERAL (` + estimateSQL(`SELECT n.id,n.fields,f.kind_slug FROM nodes n WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=f.id`) + `) est ON true`
	}
	sql := prefix + planningCTE + `, ordered AS (SELECT f.id,row_number() OVER (ORDER BY ` + listOrder(q) + `) AS rn` + orderedLead + ` FROM filtered f` + people + etaJoin + estimateJoin + planningJoin + `),
    selected AS (SELECT * FROM ordered WHERE rn>coalesce((SELECT rn FROM ordered WHERE id=` + anchorArg + `::uuid),0) ORDER BY rn LIMIT ` + limitArg + `),
    -- Count visible children for the page once instead of rescanning nodes per row.
    child_counts AS MATERIALIZED (
        SELECT c.parent_id,count(*)::int AS child_count FROM nodes c
        JOIN selected s ON s.id=c.parent_id
        WHERE c.tenant_id=current_setting('aeon.tenant_id')::uuid AND c.deleted_at IS NULL
        GROUP BY c.parent_id
    )
    SELECT ` + nodeCols + `,k.slug,k.label,nullif(n.fields->>'priority',''),assignee.id::text,assignee.name,
           ` + hasAvatar("assignee.id") + `,
           par.id::text,par.key,par.title,pk.slug,
           coalesce(cc.child_count,0),
           project.id::text,project.key,project.title,
           epic.id::text,epic.key,epic.title,` + leadColumns + moneyCols + `
    FROM selected s JOIN nodes n ON n.id=s.id JOIN node_kinds k ON k.id=n.kind_id
    LEFT JOIN child_counts cc ON cc.parent_id=n.id
    ` + assigneeJoin + pageWorker + `
    LEFT JOIN nodes par ON par.id=n.parent_id AND par.deleted_at IS NULL
    LEFT JOIN node_kinds pk ON pk.id=par.kind_id
    LEFT JOIN LATERAL (
        WITH RECURSIVE ancestors AS (
            SELECT n.id,n.parent_id,n.kind_id,n.key,n.title,0 AS depth
            UNION ALL SELECT a.id,a.parent_id,a.kind_id,a.key,a.title,anc.depth+1 FROM nodes a JOIN ancestors anc ON a.id=anc.parent_id WHERE a.deleted_at IS NULL
        ) SELECT a.id,a.key,a.title FROM ancestors a JOIN node_kinds ak ON ak.id=a.kind_id WHERE ak.slug='project' ORDER BY a.depth LIMIT 1
    ) project ON true
    LEFT JOIN LATERAL (
        -- The nearest epic above the item; the walk stops at the first epic or project.
        WITH RECURSIVE up AS (
            SELECT par.id,par.parent_id,par.kind_id,par.key,par.title,1 AS depth WHERE par.id IS NOT NULL
            UNION ALL SELECT a.id,a.parent_id,a.kind_id,a.key,a.title,up.depth+1 FROM up
                JOIN node_kinds uk ON uk.id=up.kind_id AND uk.slug NOT IN ('epic','project')
                JOIN nodes a ON a.id=up.parent_id AND a.deleted_at IS NULL
            WHERE up.depth<32
        ) SELECT u.id,u.key,u.title FROM up u JOIN node_kinds ek ON ek.id=u.kind_id WHERE ek.slug='epic' ORDER BY u.depth LIMIT 1
    ) epic ON true` + moneyJoin + `
    ORDER BY s.rn`
	return sql, args
}
func facetSQL(q listQuery) (string, []any) {
	prefix, args := listFilterSQL(q, false)
	args = append(args, q.FacetNames)
	names := fmt.Sprintf("$%d", len(args))
	want := func(name string) bool {
		for _, f := range q.FacetNames {
			if f == name {
				return true
			}
		}
		return false
	}
	// Tag, cost unit and release counts read fields per row; they are only part
	// of the query when asked for.
	extra := ""
	if want("tag") {
		extra += `
    UNION ALL SELECT 'tag',coalesce(nullif(btrim(CASE jsonb_typeof(t) WHEN 'string' THEN t#>>'{}' ELSE t->>'name' END),''),'none')
        FROM filtered f JOIN nodes n ON n.id=f.id
        LEFT JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(n.fields->'tags')='array' THEN n.fields->'tags' ELSE '[]'::jsonb END) t ON true`
	}
	if want("cost_unit") {
		extra += `
    UNION ALL SELECT 'cost_unit',coalesce(nullif(` + labelSQL("cost_unit") + `,''),'none') FROM filtered f JOIN nodes n ON n.id=f.id`
	}
	if want("release") {
		extra += `
    UNION ALL SELECT 'release',coalesce(nullif(` + labelSQL("release") + `,''),'none') FROM filtered f JOIN nodes n ON n.id=f.id`
	}
	sql := prefix + `, facet_values AS (
    SELECT 'state' AS name,f.state AS value FROM filtered f
    UNION ALL SELECT 'kind',f.kind_slug FROM filtered f
    UNION ALL SELECT 'priority',f.priority FROM filtered f
    UNION ALL SELECT 'assignee',coalesce(f.assignee_id,'none') FROM filtered f` + extra + `
    ) SELECT name,value,count(*)::int FROM facet_values WHERE name=ANY(` + names + `::text[]) GROUP BY name,value`
	return sql, args
}

func parseTreeQuery(r *http.Request) (treeQuery, error) {
	q := r.URL.Query()
	out := treeQuery{Limit: 50}
	if q.Has("root_id") {
		id, ok := parseUUID(q.Get("root_id"))
		if !ok {
			return treeQuery{}, badRequest("invalid root_id")
		}
		out.RootID = &id
	}
	if q.Has("max_depth") {
		n, err := strconv.Atoi(q.Get("max_depth"))
		if err != nil || n < 0 {
			return treeQuery{}, badRequest("invalid max_depth")
		}
		out.DepthSet = true
		out.MaxDepth = n
	}
	limit, err := parseLimit(q.Get("limit"), q.Has("limit"))
	if err != nil {
		return treeQuery{}, err
	}
	out.Limit = limit
	out.Cursor = q.Get("cursor")
	return out, nil
}

func parseLimit(raw string, present bool) (int, error) {
	if !present || raw == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 500 {
		return 0, badRequest("invalid limit")
	}
	return n, nil
}

func (m *Module) nodeTree(ctx context.Context, tenantID string, q treeQuery) (treePage, error) {
	var mark *treeMark
	if q.Cursor != "" {
		env, err := openCursor(q.Cursor)
		if err != nil {
			return treePage{}, err
		}
		if env.T != "tree" {
			return treePage{}, badRequest("cursor does not match this query")
		}
		var decoded treeMark
		if err := json.Unmarshal(env.P, &decoded); err != nil {
			return treePage{}, badRequest("invalid cursor")
		}
		if !sameString(decoded.RootID, q.RootID) || decoded.DepthSet != q.DepthSet || (q.DepthSet && decoded.MaxDepth != q.MaxDepth) {
			return treePage{}, badRequest("cursor does not match this query")
		}
		if len(decoded.PosPath) == 0 || len(decoded.PosPath) != len(decoded.IDPath) {
			return treePage{}, badRequest("invalid cursor")
		}
		mark = &decoded
	}
	page := treePage{Items: []treeEntry{}}
	err := m.tx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if q.RootID != nil {
			if _, err := loadNode(ctx, tx, *q.RootID, false); err != nil {
				return err
			}
		}
		var depth any
		if q.DepthSet {
			depth = q.MaxDepth
		}
		args := []any{q.RootID, depth}
		var posPath, idPath any
		if mark != nil {
			posPath = mark.PosPath
			idPath = mark.IDPath
		}
		args = append(args, posPath, idPath, q.Limit+1)
		rows, err := tx.Query(ctx, `
			WITH RECURSIVE walk AS (
				SELECT n.id, 0 AS depth,
					ARRAY[n.position]::numeric[] AS pos_path,
					ARRAY[n.id]::uuid[] AS id_path
				FROM nodes n
				WHERE n.deleted_at IS NULL
				  AND (($1::uuid IS NULL AND n.parent_id IS NULL) OR n.id = $1::uuid)
				UNION ALL
				SELECT c.id, w.depth + 1, w.pos_path || c.position, w.id_path || c.id
				FROM nodes c
				JOIN walk w ON c.parent_id = w.id
				WHERE c.deleted_at IS NULL
				  AND ($2::int IS NULL OR w.depth < $2::int)
			)
			SELECT `+nodeCols+`, w.depth, w.pos_path::text[], w.id_path::text[]
			FROM walk w
			JOIN nodes n ON n.id = w.id
			WHERE $3::text[] IS NULL
			   OR (w.pos_path, w.id_path) > ($3::text[]::numeric[], $4::text[]::uuid[])
			ORDER BY w.pos_path, w.id_path
			LIMIT $5`, args...)
		if err != nil {
			return dbErr("node tree", err)
		}
		defer rows.Close()
		type walked struct {
			node    nodeJSON
			depth   int
			posPath []string
			idPath  []string
		}
		var items []walked
		for rows.Next() {
			var item walked
			var fields, position string
			if err := rows.Scan(
				&item.node.ID, &item.node.Key, &item.node.KindID, &item.node.Title, &item.node.Body,
				&fields, &item.node.State, &item.node.ParentID, &position,
				&item.node.CreatedAt, &item.node.UpdatedAt, &item.node.DeletedAt,
				&item.depth, &item.posPath, &item.idPath,
			); err != nil {
				return err
			}
			if fields == "" {
				fields = "{}"
			}
			item.node.Fields = json.RawMessage(fields)
			item.node.Position = trimDecimal(position)
			item.posPath = trimPath(item.posPath)
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) > q.Limit {
			last := items[q.Limit-1]
			items = items[:q.Limit]
			encoded, err := encodeTyped("tree", treeMark{
				RootID: q.RootID, DepthSet: q.DepthSet, MaxDepth: q.MaxDepth,
				PosPath: last.posPath, IDPath: last.idPath,
			})
			if err != nil {
				return err
			}
			page.NextCursor = &encoded
		}
		page.Items = make([]treeEntry, len(items))
		for i, item := range items {
			page.Items[i] = treeEntry{Node: item.node, Depth: item.depth}
		}
		return nil
	})
	return page, err
}

func trimPath(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = trimDecimal(s)
	}
	return out
}

type projectSummary struct {
	ID           string    `json:"id"`
	Key          string    `json:"key"`
	Title        string    `json:"title"`
	State        string    `json:"state"`
	Open         int       `json:"open"`
	InProgress   int       `json:"in_progress"`
	Done         int       `json:"done"`
	Cancelled    int       `json:"cancelled"`
	Total        int       `json:"total"`
	LastActivity time.Time `json:"last_activity"`
	// The people (and agents) most recently active in the project, newest first:
	// actors of the tenant's latest events (90 days, at most 20000) on the project
	// or anything below it, a linked principal shown as the person it links to.
	People []projectPerson `json:"people"`
}
type projectPerson struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	HasAvatar bool   `json:"has_avatar"`
}

// recentPeoplePerProject bounds the avatars a project summary carries.
const recentPeoplePerProject = 5

type projectPage struct {
	Items []projectSummary `json:"items"`
}

func (m *Module) handleListProjects(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	includeArchived, err := queryBool(r, "include_archived")
	if err != nil {
		writeErr(w, err)
		return
	}
	page := projectPage{Items: []projectSummary{}}
	err = m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            WITH RECURSIVE projects AS (
                SELECT n.id,n.key,n.title,n.state,n.updated_at
                FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id
                WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.deleted_at IS NULL AND k.slug='project' AND ($1::bool OR n.state<>'archived')
            ), subtree AS (
                SELECT p.id AS project_id,p.id AS node_id,n.parent_id,n.state,n.updated_at,n.kind_id,0 AS depth
                FROM projects p JOIN nodes n ON n.id=p.id
                UNION ALL
                SELECT s.project_id,c.id,c.parent_id,c.state,c.updated_at,c.kind_id,s.depth+1
                FROM subtree s JOIN nodes c ON c.tenant_id=current_setting('aeon.tenant_id')::uuid
                    AND c.parent_id=s.node_id AND c.deleted_at IS NULL
            ), `+workStateCategoryCTE()+`, counted AS (
                SELECT p.id,p.key,p.title,p.state,s.depth,k.slug,s.updated_at,
                    `+workCountBucketSQL("s.state", "c")+` AS bucket
                FROM projects p JOIN subtree s ON s.project_id=p.id
                JOIN node_kinds k ON k.id=s.kind_id AND k.tenant_id=current_setting('aeon.tenant_id')::uuid
                LEFT JOIN configured c ON c.kind_id=s.kind_id AND c.norm=`+workStateNormSQL("s.state")+`
            ), summary AS (
                SELECT id,key,title,state,
                    count(*) FILTER (WHERE depth>0 AND slug IN ('ticket','task','epic') AND bucket='open')::int AS open,
                    count(*) FILTER (WHERE depth>0 AND slug IN ('ticket','task','epic') AND bucket='in_progress')::int AS in_progress,
                    count(*) FILTER (WHERE depth>0 AND slug IN ('ticket','task','epic') AND bucket='done')::int AS done,
                    count(*) FILTER (WHERE depth>0 AND slug IN ('ticket','task','epic') AND bucket='cancelled')::int AS cancelled,
                    count(*) FILTER (WHERE depth>0 AND slug IN ('ticket','task','epic'))::int AS total,
                    max(updated_at) AS last_activity
                FROM counted
                GROUP BY id,key,title,state
            ), recent AS (
                SELECT e.node_id,e.actor_principal_id,e.at FROM events e
                WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND e.node_id IS NOT NULL AND e.at > now() - interval '90 days'
                ORDER BY e.at DESC,e.id DESC LIMIT 20000
            ), actors AS (
                SELECT s.project_id,coalesce(a.linked_to,a.id) AS person,max(r.at) AS at
                FROM recent r JOIN subtree s ON s.node_id=r.node_id
                JOIN principals a ON a.tenant_id=current_setting('aeon.tenant_id')::uuid AND a.id=r.actor_principal_id
                GROUP BY s.project_id,coalesce(a.linked_to,a.id)
            ), ranked AS (
                SELECT project_id,person,at,row_number() OVER (PARTITION BY project_id ORDER BY at DESC,person) AS n FROM actors
            ), people AS (
                SELECT r.project_id,json_agg(json_build_object('id',who.id::text,'name',who.name,'kind',who.kind,'has_avatar',`+hasAvatar("who.id")+`) ORDER BY r.at DESC,r.person) AS people
                FROM ranked r JOIN principals who ON who.tenant_id=current_setting('aeon.tenant_id')::uuid AND who.id=r.person
                WHERE r.n <= $2
                GROUP BY r.project_id
            )
            SELECT s.id::text,s.key,s.title,s.state,s.open,s.in_progress,s.done,s.cancelled,s.total,s.last_activity,coalesce(pp.people,'[]'::json)
            FROM summary s LEFT JOIN people pp ON pp.project_id=s.id
            ORDER BY s.last_activity DESC,s.id`, includeArchived, recentPeoplePerProject)
		if err != nil {
			return dbErr("list projects", err)
		}
		for rows.Next() {
			var item projectSummary
			var people []byte
			if err := rows.Scan(&item.ID, &item.Key, &item.Title, &item.State, &item.Open, &item.InProgress, &item.Done, &item.Cancelled, &item.Total, &item.LastActivity, &people); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(people, &item.People); err != nil || item.People == nil {
				item.People = []projectPerson{}
			}
			page.Items = append(page.Items, item)
		}
		err = rows.Err()
		rows.Close()
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
