// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/workqueue"
)

// issueView is the classic issue text/JSON shape. Aeon stores the issue as a
// node: type is the kind slug, status is state, and priority lives in fields.
type issueView struct {
	Queued              *workqueue.Queued `json:"queued,omitempty"`
	EstimateHours       *float64          `json:"estimate_hours,omitempty"`
	EstimateSource      string            `json:"estimate_source,omitempty"`
	EstimateBy          string            `json:"estimate_by,omitempty"`
	EstimateAt          string            `json:"estimate_at,omitempty"`
	PillEN              string            `json:"pill_en,omitempty"`
	PillDE              string            `json:"pill_de,omitempty"`
	BenefitEN           string            `json:"benefit_en,omitempty"`
	BenefitDE           string            `json:"benefit_de,omitempty"`
	Hide                bool              `json:"hide_from_release_notes,omitempty"`
	Warnings            []string          `json:"warnings,omitempty"`
	IssueKey            string            `json:"issue_key"`
	Title               string            `json:"title"`
	Type                string            `json:"type"`
	Status              string            `json:"status"`
	Priority            string            `json:"priority"`
	RouteRole           string            `json:"route_role,omitempty"`
	RouteRoleSource     string            `json:"route_role_source,omitempty"`
	RouteRoleBy         string            `json:"route_role_by,omitempty"`
	RouteRoleAt         string            `json:"route_role_at,omitempty"`
	Area                string            `json:"area,omitempty"`
	AreaSource          string            `json:"area_source,omitempty"`
	AreaBy              string            `json:"area_by,omitempty"`
	AreaAt              string            `json:"area_at,omitempty"`
	RouteRoleConfirmed  *bool             `json:"route_role_confirmed,omitempty"`
	AreaConfirmed       *bool             `json:"area_confirmed,omitempty"`
	Complexity          string            `json:"complexity,omitempty"`
	ComplexitySource    string            `json:"complexity_source,omitempty"`
	ComplexityBy        string            `json:"complexity_by,omitempty"`
	ComplexityAt        string            `json:"complexity_at,omitempty"`
	ComplexityConfirmed *bool             `json:"complexity_confirmed,omitempty"`
	Description         string            `json:"description,omitempty"`
	ID                  string            `json:"id"`
	Assignee            string            `json:"assignee,omitempty"`
	Tags                []string          `json:"tags,omitempty"`
	Comments            []string          `json:"comments,omitempty"`
}

type issueInput struct {
	Estimate    string
	Benefits    benefitFlags
	Project     string
	Title       string
	Type        string
	Status      string
	Priority    string
	Parent      string
	Assignee    string
	Description string
	AC          string
	Notes       string
	Tags        []string
}

var issueKinds = map[string]bool{"epic": true, "ticket": true, "task": true}

func (rt *runtime) viewIssue(n apiNode, kinds kindTable) issueView {
	fields := fieldMap(n.Fields)
	var comments []string
	if raw, ok := fields["comments"].([]any); ok {
		for _, item := range raw {
			body, _ := item.(map[string]any)
			if text := fieldString(body, "body"); text != "" {
				comments = append(comments, text)
			}
		}
	}
	hidden, _ := fields["hide_from_release_notes"].(bool)
	var estimate *float64
	if h, ok := fields["estimate_hours"].(float64); ok && validEstimate(h) {
		estimate = &h
	}
	return issueView{
		Queued:        n.Queued,
		EstimateHours: estimate, EstimateSource: fieldString(fields, "estimate_source"), EstimateBy: fieldString(fields, "estimate_by"), EstimateAt: fieldString(fields, "estimate_at"),
		PillEN: fieldString(fields, "pill_en"), PillDE: fieldString(fields, "pill_de"), BenefitEN: fieldString(fields, "benefit_en"), BenefitDE: fieldString(fields, "benefit_de"), Hide: hidden, Warnings: n.Warnings,
		IssueKey:            n.Key,
		Title:               n.Title,
		Type:                kinds.slug(n.KindID),
		Status:              n.State,
		Priority:            fieldString(fields, "priority"),
		RouteRole:           fieldString(fields, "route_role"),
		RouteRoleSource:     fieldString(fields, "route_role_source"),
		RouteRoleBy:         fieldString(fields, "route_role_by"),
		RouteRoleAt:         fieldString(fields, "route_role_at"),
		Area:                fieldString(fields, "area"),
		AreaSource:          fieldString(fields, "area_source"),
		AreaBy:              fieldString(fields, "area_by"),
		AreaAt:              fieldString(fields, "area_at"),
		RouteRoleConfirmed:  fieldBoolPointer(fields, "route_role_confirmed"),
		AreaConfirmed:       fieldBoolPointer(fields, "area_confirmed"),
		Complexity:          fieldString(fields, "complexity"),
		ComplexitySource:    fieldString(fields, "complexity_source"),
		ComplexityBy:        fieldString(fields, "complexity_by"),
		ComplexityAt:        fieldString(fields, "complexity_at"),
		ComplexityConfirmed: fieldBoolPointer(fields, "complexity_confirmed"),
		Description:         n.Body,
		ID:                  n.ID,
		Assignee:            fieldString(fields, "assignee"),
		Tags:                fieldStrings(fields, "tags"),
		Comments:            comments,
	}
}

func (rt *runtime) printIssue(v issueView) error {
	if rt.jsonOut {
		return rt.printJSON(v)
	}
	fmt.Fprintf(rt.stdout, "%s  %s\n", v.IssueKey, v.Title)
	fmt.Fprintf(rt.stdout, "  type:     %s\n", v.Type)
	fmt.Fprintf(rt.stdout, "  status:   %s\n", v.Status)
	fmt.Fprintf(rt.stdout, "  priority: %s\n", v.Priority)
	if v.EstimateHours != nil {
		fmt.Fprintf(rt.stdout, "  estimate: %gh (%s)\n", *v.EstimateHours, v.EstimateSource)
	}
	if v.RouteRole != "" {
		fmt.Fprintf(rt.stdout, "  role:     %s\n", routeProvenance(v.RouteRole, v.RouteRoleSource))
	}
	if v.Area != "" {
		fmt.Fprintf(rt.stdout, "  area:     %s\n", routeProvenance(v.Area, v.AreaSource))
	}
	if v.Complexity != "" {
		fmt.Fprintf(rt.stdout, "  complexity: %s\n", routeProvenance(v.Complexity, v.ComplexitySource))
	}
	if v.Description != "" {
		desc := clipRunes(v.Description, 160, "…")
		fmt.Fprintf(rt.stdout, "\n  %s\n", strings.ReplaceAll(desc, "\n", "\n  "))
	}
	return nil
}

func (rt *runtime) printIssueList(items []issueView, total int) error {
	if rt.jsonOut {
		return rt.printJSON(map[string]any{"issues": items, "total": total})
	}
	if len(items) == 0 {
		fmt.Fprintln(rt.stdout, "(no issues)")
		return nil
	}
	fmt.Fprintln(rt.stdout, "KEY           STATUS         PRIO   TITLE")
	for _, item := range items {
		fmt.Fprintf(rt.stdout, "%-13s %-14s %-6s %s\n", item.IssueKey, item.Status, item.Priority, clipRunes(item.Title, 60, "…"))
	}
	if total > len(items) {
		fmt.Fprintf(rt.stdout, "\n(showing %d of %d — use --limit / --offset for more)\n", len(items), total)
	}
	return nil
}

func (rt *runtime) listIssues(project, status, typ, priority, assignee string, limit, offset int) error {
	if typ != "" && !issueKinds[typ] {
		return usagef("unknown issue type %q", typ)
	}
	proj, err := rt.projectNode(project)
	if err != nil {
		return err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	q := url.Values{"parent_id": {proj.ID}, "include_descendants": {"true"}}
	if status != "" {
		q.Set("state", status)
	}
	if typ != "" {
		k, ok := kinds.bySlug[typ]
		if !ok {
			return rt.fail(fmt.Errorf("node kind %q is not configured", typ), "")
		}
		q.Set("kind_id", k.ID)
	}
	nodes, err := rt.walkNodes(q, nil)
	if err != nil {
		return err
	}
	var matched []issueView
	for _, n := range nodes {
		slug := kinds.slug(n.KindID)
		if typ == "" && !issueKinds[slug] {
			continue
		}
		fields := fieldMap(n.Fields)
		if priority != "" && fieldString(fields, "priority") != priority {
			continue
		}
		if assignee != "" && fieldString(fields, "assignee") != assignee {
			continue
		}
		matched = append(matched, rt.viewIssue(n, kinds))
	}
	if limit <= 0 {
		limit = 50
	}
	if offset > len(matched) {
		offset = len(matched)
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	return rt.printIssueList(matched[offset:end], len(matched))
}

func (rt *runtime) getIssue(ref string) error {
	if strings.HasPrefix(strings.TrimSpace(ref), "id:") {
		return usagef("id:<n> is a classic numeric id; pass the issue key")
	}
	n, err := rt.nodeByKey(ref)
	if err != nil {
		return err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	if !issueKinds[kinds.slug(n.KindID)] {
		return rt.fail(fmt.Errorf("issue %q not found", ref), "")
	}
	view := rt.viewIssue(n, kinds)
	comments, err := rt.issueComments(n.ID)
	if err != nil {
		return err
	}
	view.Comments = append(view.Comments, comments...)
	return rt.printIssue(view)
}

func (rt *runtime) issueComments(nodeID string) ([]string, error) {
	var comments []string
	cursor := ""
	for page := 0; page < 50; page++ {
		path := "/api/nodes/" + url.PathEscape(nodeID) + "/activity?limit=200"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var result struct {
			Items []struct {
				Type string `json:"type"`
				Body string `json:"body_markdown"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		if err := rt.do(http.MethodGet, path, nil, &result); err != nil {
			return nil, err
		}
		for _, item := range result.Items {
			if item.Type == "comment" {
				comments = append(comments, item.Body)
			}
		}
		if result.NextCursor == nil || *result.NextCursor == "" {
			return comments, nil
		}
		if *result.NextCursor == cursor {
			return nil, rt.fail(fmt.Errorf("activity cursor did not advance"), "")
		}
		cursor = *result.NextCursor
	}
	return nil, rt.fail(fmt.Errorf("issue activity exceeds 10000 entries"), "")
}

func (rt *runtime) createIssue(in issueInput) error {
	kindName := strings.TrimSpace(in.Type)
	if kindName == "" {
		kindName = "ticket"
	}
	if !issueKinds[kindName] {
		return usagef("unknown issue type %q", kindName)
	}
	prefix := strings.TrimSpace(in.Project)
	if !validKeyPrefix(prefix) {
		return usagef("project key %q cannot allocate issue keys", prefix)
	}
	proj, err := rt.projectNode(prefix)
	if err != nil {
		return err
	}
	kind, err := rt.kind(kindName)
	if err != nil {
		return err
	}
	parentID := proj.ID
	if strings.TrimSpace(in.Parent) != "" {
		if strings.HasPrefix(strings.TrimSpace(in.Parent), "id:") {
			return usagef("id:<n> is a classic numeric id; pass the issue key")
		}
		parentRef, err := normalizeIssueRef(in.Parent)
		if err != nil {
			return err
		}
		parent, err := rt.nodeByKey(parentRef)
		if err != nil {
			return err
		}
		parentID = parent.ID
	}
	fields := map[string]any{}
	if in.Estimate != "" {
		hours, err := parseEstimate(in.Estimate)
		if err != nil {
			return err
		}
		estimateFields(fields, hours, "")
	}
	if _, err := in.Benefits.apply(fields); err != nil {
		return err
	}
	if p := strings.TrimSpace(in.Priority); p != "" {
		fields["priority"] = p
	}
	if a := strings.TrimSpace(in.Assignee); a != "" {
		fields["assignee"] = a
	}
	if in.AC != "" {
		fields["acceptance_criteria"] = in.AC
	}
	if in.Notes != "" {
		fields["notes"] = in.Notes
	}
	if len(in.Tags) > 0 {
		fields["tags"] = in.Tags
	}
	body := map[string]any{
		"kind_id":    kind.ID,
		"title":      strings.TrimSpace(in.Title),
		"body":       in.Description,
		"parent_id":  parentID,
		"key_prefix": prefix,
		"fields":     fields,
	}
	if status := strings.TrimSpace(in.Status); status != "" {
		body["state"] = status
	}
	var created apiNode
	if err := rt.do(http.MethodPost, "/api/nodes", body, &created); err != nil {
		return err
	}
	if (kindName == "ticket" || kindName == "task") && in.Estimate == "" && !validEstimate(fieldMap(created.Fields)["estimate_hours"]) {
		found := false
		for _, warning := range created.Warnings {
			found = found || strings.Contains(warning, "fields.estimate_hours")
		}
		if !found {
			created.Warnings = append(created.Warnings, "add an agent-hours estimate in fields.estimate_hours")
		}
	}
	created.Warnings = withEstimateHints(created.Warnings)
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	view := rt.viewIssue(created, kinds)
	for _, warning := range created.Warnings {
		fmt.Fprintln(rt.stderr, "warning:", warning)
	}
	if rt.jsonOut {
		return rt.printJSON(view)
	}
	fmt.Fprintf(rt.stdout, "✓ created %s — %s\n", view.IssueKey, view.Title)
	return nil
}

type issuePatch struct {
	Estimate       string
	EstimateSource string
	Benefits       benefitFlags
	Ref            string
	Title          string
	Type           string
	Status         string
	Priority       string
	Parent         string
	Assignee       string
	Project        string
	Description    string
	AC             string
	Notes          string
	CloseNote      string
	AddTag         []string
	RemoveTag      []string
	RouteRole      string
	Area           string
	Complexity     string
}

// noteKindUpdate refuses a different kind before any write. The same kind is a
// no-op and is reported; the caller must not send the kind to the API.
func (rt *runtime) noteKindUpdate(ref, requested string) error {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return nil
	}
	n, err := rt.nodeByKey(ref)
	if err != nil {
		return err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	current := kinds.slug(n.KindID)
	if current == requested {
		fmt.Fprintf(rt.stdout, "kind is already %s\n", current)
		return nil
	}
	return rt.fail(fmt.Errorf("kind_change_not_allowed: %s → %s; use \"aeon issue convert %s --to %s\"", current, requested, n.Key, requested), "")
}

func (rt *runtime) updateIssue(in issuePatch) error {
	if strings.HasPrefix(strings.TrimSpace(in.Ref), "id:") {
		return usagef("id:<n> is a classic numeric id; pass the issue key")
	}
	if err := rt.noteKindUpdate(in.Ref, in.Type); err != nil {
		return err
	}
	n, err := rt.nodeByKey(in.Ref)
	if err != nil {
		return err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	if !issueKinds[kinds.slug(n.KindID)] {
		return rt.fail(fmt.Errorf("issue %q not found", in.Ref), "")
	}
	if in.RouteRole != "" || in.Area != "" || in.Complexity != "" {
		if slug := kinds.slug(n.KindID); slug != "ticket" && slug != "task" {
			return usagef("--role, --area and --complexity apply to tickets and tasks")
		}
	}
	fields := fieldMap(n.Fields)
	changedFields, err := in.Benefits.apply(fields)
	if in.Estimate != "" {
		hours, parseErr := parseEstimate(in.Estimate)
		if parseErr != nil {
			return parseErr
		}
		estimateFields(fields, hours, in.EstimateSource)
		changedFields = true
	}
	if err != nil {
		return err
	}
	if p := strings.TrimSpace(in.Priority); p != "" {
		fields["priority"] = p
		changedFields = true
	}
	if in.RouteRole != "" || in.Area != "" || in.Complexity != "" {
		applyRouteFields(fields, in.RouteRole, in.Area, in.Complexity)
		changedFields = true
	}
	if a := strings.TrimSpace(in.Assignee); a != "" {
		fields["assignee"] = a
		changedFields = true
	}
	if in.AC != "" {
		fields["acceptance_criteria"] = in.AC
		changedFields = true
	}
	if in.Notes != "" {
		fields["notes"] = in.Notes
		changedFields = true
	}
	if in.CloseNote != "" {
		fields["close_note"] = in.CloseNote
		changedFields = true
	}
	if len(in.AddTag) > 0 || len(in.RemoveTag) > 0 {
		tags := fieldStrings(fields, "tags")
		drop := map[string]bool{}
		for _, tag := range in.RemoveTag {
			drop[tag] = true
		}
		var next []string
		seen := map[string]bool{}
		for _, tag := range tags {
			if drop[tag] || seen[tag] {
				continue
			}
			seen[tag] = true
			next = append(next, tag)
		}
		for _, tag := range in.AddTag {
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			next = append(next, tag)
		}
		fields["tags"] = next
		changedFields = true
	}
	patch := map[string]any{}
	if title := strings.TrimSpace(in.Title); title != "" {
		patch["title"] = title
	}
	if in.Description != "" {
		patch["body"] = in.Description
	}
	if status := strings.TrimSpace(in.Status); status != "" {
		patch["state"] = status
	}
	if changedFields {
		patch["fields"] = fields
	}
	oldStatus := n.State
	if len(patch) > 0 {
		if n.UpdatedAt.IsZero() {
			return usagef("%s has no revision timestamp; nothing was written", n.Key)
		}
		if err := rt.doHeaders(http.MethodPatch, "/api/nodes/"+url.PathEscape(n.ID), patch, &n, map[string]string{"If-Unmodified-Since": n.UpdatedAt.Format(time.RFC3339Nano)}); err != nil {
			if stale, ok := err.(*exitError); ok && strings.Contains(stale.msg, "api 412:") {
				stale.msg += "; nothing was written"
			}
			return err
		}
	}
	if project := strings.TrimSpace(in.Project); project != "" {
		proj, err := rt.projectNode(project)
		if err != nil {
			return err
		}
		var moved movedIssue
		if err := rt.do(http.MethodPost, "/api/nodes/"+url.PathEscape(n.ID)+"/project-move", map[string]any{"project_id": proj.ID}, &moved); err != nil {
			return err
		}
		if err := rt.do(http.MethodGet, "/api/nodes/"+url.PathEscape(n.ID), nil, &n); err != nil {
			return err
		}
	}
	if parent := strings.TrimSpace(in.Parent); parent != "" {
		if strings.HasPrefix(parent, "id:") {
			return usagef("id:<n> is a classic numeric id; pass the issue key")
		}
		ref, err := normalizeIssueRef(parent)
		if err != nil {
			return err
		}
		target, err := rt.nodeByKey(ref)
		if err != nil {
			return err
		}
		if n.ParentID == nil || *n.ParentID != target.ID {
			if err := rt.do(http.MethodPost, "/api/nodes/"+url.PathEscape(n.ID)+"/move", map[string]any{"parent_id": target.ID}, &n); err != nil {
				return err
			}
		}
	}
	view := rt.viewIssue(n, kinds)
	if rt.jsonOut {
		return rt.printJSON(view)
	}
	if strings.TrimSpace(in.Status) != "" && strings.TrimSpace(in.Title+in.Description+in.Priority+in.Assignee+in.Project+in.Parent+in.AC+in.Notes+in.CloseNote+in.RouteRole+in.Area) == "" && len(in.AddTag) == 0 && len(in.RemoveTag) == 0 {
		fmt.Fprintf(rt.stdout, "✓ %s: %s → %s\n", view.IssueKey, oldStatus, view.Status)
		return nil
	}
	fmt.Fprintf(rt.stdout, "✓ updated %s\n", view.IssueKey)
	return nil
}

// convertIssue never converts. The CLI holds an agent key, and kind conversion
// is a person action in the web app. Lookups are read-only and only build the
// link and check that --to is an issue-family kind.
func (rt *runtime) convertIssue(ref, to string) error {
	if strings.HasPrefix(strings.TrimSpace(ref), "id:") {
		return usagef("id:<n> is a classic numeric id; pass the issue key")
	}
	n, nodeErr := rt.nodeByKey(ref)
	kinds, kindsErr := rt.loadKinds()
	if kindsErr == nil {
		if err := requireIssueFamilyTarget(kinds, to); err != nil {
			return err
		}
	}
	key := strings.TrimSpace(ref)
	if nodeErr == nil && n.Key != "" {
		key = n.Key
	}
	return &exitError{code: 3, msg: fmt.Sprintf("Converting a kind needs a person. Open %s, then ⋯ → Convert to %s.", rt.ticketWebURL(key), to)}
}

func requireIssueFamilyTarget(kinds kindTable, to string) error {
	kind, ok := kinds.bySlug[to]
	if !ok || !issueFamilyKind(kind) {
		return usagef("--to %q is not an issue kind", to)
	}
	return nil
}

func (rt *runtime) commentIssue(ref, body string) error {
	if strings.HasPrefix(strings.TrimSpace(ref), "id:") {
		return usagef("id:<n> is a classic numeric id; pass the issue key")
	}
	n, err := rt.nodeByKey(ref)
	if err != nil {
		return err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	if !issueKinds[kinds.slug(n.KindID)] {
		return rt.fail(fmt.Errorf("issue %q not found", ref), "")
	}
	var comment map[string]any
	if err := rt.do(http.MethodPost, "/api/nodes/"+url.PathEscape(n.ID)+"/comments", map[string]any{"body_markdown": body}, &comment); err != nil {
		return err
	}
	if rt.jsonOut {
		return rt.printJSON(comment)
	}
	fmt.Fprintf(rt.stdout, "✓ commented on %s\n", n.Key)
	return nil
}

func validKeyPrefix(s string) bool {
	if len(s) < 2 || len(s) > 10 {
		return false
	}
	for i, r := range s {
		if i == 0 && (r < 'A' || r > 'Z') {
			return false
		}
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

type knowledgeView struct {
	ID               string         `json:"id"`
	ProjectID        string         `json:"project_id"`
	Type             string         `json:"type"`
	Slug             string         `json:"slug"`
	Title            string         `json:"title"`
	Body             string         `json:"body"`
	Status           string         `json:"status"`
	Metadata         map[string]any `json:"metadata"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
	ReferenceCount   int64          `json:"reference_count"`
	LastReferencedAt string         `json:"last_referenced_at,omitempty"`
	Key              string         `json:"-"`
}

func knowledgeSupported(typ string) bool {
	_, ok := knowledgeKindSlug(typ)
	return ok
}

func (rt *runtime) rejectKnowledgeKind(typ string) error {
	if knowledgeSupported(typ) {
		return nil
	}
	return notYet(reasonKnowledgeKind)
}

func viewKnowledge(n apiNode, kinds kindTable) knowledgeView {
	fields := fieldMap(n.Fields)
	metadata, _ := fields["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	projectID := ""
	if n.ParentID != nil {
		projectID = *n.ParentID
	}
	return knowledgeView{
		ID:        n.ID,
		ProjectID: projectID,
		Type:      knowledgeCLIType(kinds.slug(n.KindID)),
		Slug:      fieldString(fields, "slug"),
		Title:     n.Title,
		Status:    n.State,
		Body:      n.Body,
		Metadata:  metadata,
		CreatedAt: n.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: n.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Key:       n.Key,
	}
}

func (rt *runtime) printKnowledge(v knowledgeView) error {
	if rt.jsonOut {
		return rt.printJSON(v)
	}
	fmt.Fprintf(rt.stdout, "%s/%s (#%s)\n", v.Type, v.Slug, v.ID)
	fmt.Fprintf(rt.stdout, "  title:  %s\n", v.Title)
	fmt.Fprintf(rt.stdout, "  status: %s\n", v.Status)
	if len(v.Metadata) > 0 {
		if raw, err := json.MarshalIndent(v.Metadata, "  ", "  "); err == nil {
			fmt.Fprintf(rt.stdout, "  metadata: %s\n", raw)
		}
	}
	if strings.TrimSpace(v.Body) != "" {
		fmt.Fprintln(rt.stdout, "  body:")
		for _, line := range strings.Split(v.Body, "\n") {
			fmt.Fprintln(rt.stdout, "    "+line)
		}
	}
	return nil
}

func (rt *runtime) knowledgeNodes(project, typ string) (kindTable, []apiNode, error) {
	if err := rt.rejectKnowledgeKind(typ); err != nil && typ != "" {
		return kindTable{}, nil, err
	}
	proj, err := rt.projectNode(project)
	if err != nil {
		return kindTable{}, nil, err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return kindTable{}, nil, err
	}
	q := url.Values{"parent_id": {proj.ID}, "include_descendants": {"true"}}
	want := ""
	if typ != "" {
		slug, ok := knowledgeKindSlug(typ)
		if !ok {
			return kindTable{}, nil, rt.rejectKnowledgeKind(typ)
		}
		want = slug
		k, ok := kinds.bySlug[slug]
		if !ok {
			return kinds, nil, nil
		}
		q.Set("kind_id", k.ID)
	}
	nodes, err := rt.walkNodes(q, nil)
	if err != nil {
		return kindTable{}, nil, err
	}
	var kept []apiNode
	for _, n := range nodes {
		slug := kinds.slug(n.KindID)
		if !knowledgeSupported(slug) {
			continue
		}
		if want != "" && slug != want {
			continue
		}
		kept = append(kept, n)
	}
	return kinds, kept, nil
}

func (rt *runtime) listKnowledge(project, typ string) error {
	kinds, nodes, err := rt.knowledgeNodes(project, typ)
	if err != nil {
		return err
	}
	var items []knowledgeView
	for _, n := range nodes {
		items = append(items, viewKnowledge(n, kinds))
	}
	if rt.jsonOut {
		if items == nil {
			items = []knowledgeView{}
		}
		return rt.printJSON(items)
	}
	if len(items) == 0 {
		fmt.Fprintln(rt.stdout, "(no entries)")
		return nil
	}
	fmt.Fprintln(rt.stdout, "TYPE             SLUG                            STATUS       TITLE")
	for _, e := range items {
		fmt.Fprintf(rt.stdout, "%-16s %-31s %-12s %s\n", e.Type, e.Slug, e.Status, clipRunes(e.Title, 60, "..."))
	}
	return nil
}

func (rt *runtime) getKnowledge(typ, slug, project string) error {
	kinds, nodes, err := rt.knowledgeNodes(project, typ)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if fieldString(fieldMap(n.Fields), "slug") == slug {
			return rt.printKnowledge(viewKnowledge(n, kinds))
		}
	}
	return rt.fail(fmt.Errorf("knowledge %s/%s not found", typ, slug), "")
}

func (rt *runtime) createKnowledge(project, typ, slug, title, body, status string) error {
	if err := rt.rejectKnowledgeKind(typ); err != nil {
		return err
	}
	proj, err := rt.projectNode(project)
	if err != nil {
		return err
	}
	kindSlug, ok := knowledgeKindSlug(typ)
	if !ok {
		return rt.rejectKnowledgeKind(typ)
	}
	if err := rt.ensureKnowledgeKind(kindSlug); err != nil {
		return err
	}
	kind, err := rt.kind(kindSlug)
	if err != nil {
		return err
	}
	req := map[string]any{
		"kind_id":   kind.ID,
		"title":     strings.TrimSpace(title),
		"body":      body,
		"parent_id": proj.ID,
		"fields":    map[string]any{"slug": strings.TrimSpace(slug)},
	}
	if s := strings.TrimSpace(status); s != "" {
		req["state"] = s
	}
	var created apiNode
	if err := rt.do(http.MethodPost, "/api/nodes", req, &created); err != nil {
		return err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	view := viewKnowledge(created, kinds)
	if rt.jsonOut {
		return rt.printJSON(view)
	}
	fmt.Fprintf(rt.stdout, "✓ created %s/%s (%s)\n", view.Type, view.Slug, view.Key)
	return nil
}

func (rt *runtime) updateKnowledge(typ, slug, project, title, body, status, newSlug, metadata string) error {
	kinds, nodes, err := rt.knowledgeNodes(project, typ)
	if err != nil {
		return err
	}
	var n apiNode
	found := false
	for _, item := range nodes {
		if fieldString(fieldMap(item.Fields), "slug") == slug {
			n = item
			found = true
			break
		}
	}
	if !found {
		return rt.fail(fmt.Errorf("knowledge %s/%s not found", typ, slug), "")
	}
	fields := fieldMap(n.Fields)
	patch := map[string]any{}
	if t := strings.TrimSpace(title); t != "" {
		patch["title"] = t
	}
	if body != "" {
		patch["body"] = body
	}
	if s := strings.TrimSpace(status); s != "" {
		patch["state"] = s
	}
	if s := strings.TrimSpace(newSlug); s != "" {
		fields["slug"] = s
		patch["fields"] = fields
	}
	if metadata != "" {
		var meta map[string]any
		if err := json.Unmarshal([]byte(metadata), &meta); err != nil || meta == nil {
			return usagef("--metadata must be a JSON object")
		}
		fields["metadata"] = meta
		patch["fields"] = fields
	}
	if len(patch) == 0 {
		return usagef("nothing to update")
	}
	if err := rt.do(http.MethodPatch, "/api/nodes/"+url.PathEscape(n.ID), patch, &n); err != nil {
		return err
	}
	view := viewKnowledge(n, kinds)
	if rt.jsonOut {
		return rt.printJSON(view)
	}
	fmt.Fprintf(rt.stdout, "✓ updated %s/%s (%s)\n", view.Type, view.Slug, view.Key)
	return nil
}

func (rt *runtime) searchIssues(query, project, typ string, limit int) error {
	if typ != "" && !issueKinds[typ] {
		return usagef("unknown issue type %q", typ)
	}
	var parent string
	if strings.TrimSpace(project) != "" {
		proj, err := rt.projectNode(project)
		if err != nil {
			return err
		}
		parent = proj.ID
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	q := url.Values{"q": {query}}
	if typ != "" {
		k, ok := kinds.bySlug[typ]
		if !ok {
			return usagef("unknown issue type %q", typ)
		}
		q.Set("kind_id", k.ID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var page struct {
		Items []struct {
			Node apiNode `json:"node"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := rt.do(http.MethodGet, "/api/search?"+q.Encode(), nil, &page); err != nil {
		return err
	}
	var items []issueView
	for _, hit := range page.Items {
		slug := kinds.slug(hit.Node.KindID)
		if !issueKinds[slug] {
			continue
		}
		if parent != "" && (hit.Node.ParentID == nil || *hit.Node.ParentID != parent) {
			continue
		}
		items = append(items, rt.viewIssue(hit.Node, kinds))
	}
	if rt.jsonOut {
		return rt.printJSON(map[string]any{"issues": items, "has_more": page.NextCursor != nil && *page.NextCursor != ""})
	}
	if len(items) == 0 {
		fmt.Fprintln(rt.stdout, "(no issues)")
		return nil
	}
	fmt.Fprintln(rt.stdout, "KEY           TYPE     STATUS         TITLE")
	for _, item := range items {
		fmt.Fprintf(rt.stdout, "%-13s %-8s %-14s %s\n", item.IssueKey, item.Type, item.Status, clipRunes(item.Title, 57, "..."))
	}
	if page.NextCursor != nil && *page.NextCursor != "" {
		fmt.Fprintln(rt.stdout, "\n(more issues available; raise --limit or use --json for has_more)")
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func fieldBoolPointer(fields map[string]any, key string) *bool {
	if value, ok := fields[key].(bool); ok {
		return &value
	}
	return nil
}
