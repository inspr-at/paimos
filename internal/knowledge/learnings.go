// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// process-learning is the tag a person or an agent puts on a ticket, task, epic
// or comment to nominate it for the method-learnings inbox (AEON-275).
const (
	processLearningTag = "process-learning"
	learningListLimit  = 50
	learningScanLimit  = 500
)

var (
	processLearningToken = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_-])#?process-learning([^A-Za-z0-9_-]|$)`)
	incidentWord         = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_-])incident([^A-Za-z0-9_-]|$)`)
	atxHeading           = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)\s*$`)
	changelogWord        = regexp.MustCompile(`(?i)changelog`)
	issueKinds           = map[string]bool{"ticket": true, "task": true, "epic": true}
)

// Learning is one open method learning on the Knowledge tab.
type Learning struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	NodeID    string    `json:"node_id"`
	Key       string    `json:"key"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	CommentID string    `json:"comment_id,omitempty"`
	At        time.Time `json:"at"`
	Author    *Person   `json:"author"`
	Href      string    `json:"href"`
	projectID string
	verdict   bool
}

// LearningPage is GET /api/knowledge/learnings.
type LearningPage struct {
	Items     []Learning `json:"items"`
	Truncated bool       `json:"truncated"`
}

// learningAudit is the dismiss event. node_id and project_id are real nodes.
type learningAudit struct {
	SourceKey string `json:"source_key"`
	Source    string `json:"source"`
	NodeID    string `json:"node_id"`
	CommentID string `json:"comment_id,omitempty"`
	ProjectID string `json:"project_id"`
	Text      string `json:"text"`
}

// LearningDecision is the result of accept or dismiss.
type LearningDecision struct {
	ID           string `json:"id"`
	Decision     string `json:"decision"`
	EventID      int64  `json:"event_id"`
	KnowledgeID  string `json:"knowledge_id,omitempty"`
	Heading      string `json:"heading,omitempty"`
	Line         string `json:"line,omitempty"`
	Entry        *Entry `json:"entry,omitempty"`
	RuleSetID    string `json:"rule_set_id,omitempty"`
	RuleLayerID  string `json:"rule_layer_id,omitempty"`
	RuleIdentity string `json:"rule_identity,omitempty"`
}

func closedLearning() error {
	return fail(http.StatusNotFound, "learning_closed", "This learning is no longer open.")
}

func alreadyDecided() error {
	return fail(http.StatusConflict, "already_decided", "This learning was already accepted or dismissed.")
}

func requirePerson(p tenant.Principal) error {
	if p.Kind == tenant.Person {
		return nil
	}
	return fail(http.StatusForbidden, "person_required", "only a person can accept or dismiss a method learning")
}

// ---------- Changelog text ----------

// appendChangelog adds line at the end of the last changelog heading. When the
// body has none, it adds a Changelog section. The heading text is returned
// without its marks.
func appendChangelog(body, line string) (string, string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	lines := strings.Split(body, "\n")
	fenced := fencedLines(lines)
	type hit struct {
		index, level int
		text         string
	}
	var hits []hit
	for i, row := range lines {
		if fenced[i] {
			continue
		}
		m := atxHeading.FindStringSubmatch(strings.TrimRight(row, "\r"))
		if m == nil || !changelogWord.MatchString(m[2]) {
			continue
		}
		hits = append(hits, hit{index: i, level: len(m[1]), text: strings.TrimSpace(m[2])})
	}
	if len(hits) == 0 {
		block := "## Changelog\n\n" + line + "\n"
		trimmed := strings.TrimRight(body, "\n")
		if strings.TrimSpace(trimmed) == "" {
			return block, "Changelog"
		}
		return trimmed + "\n\n" + block, "Changelog"
	}
	last := hits[len(hits)-1]
	end := len(lines)
	for j := last.index + 1; j < len(lines); j++ {
		if fenced[j] {
			continue
		}
		m := atxHeading.FindStringSubmatch(strings.TrimRight(lines[j], "\r"))
		if m != nil && len(m[1]) <= last.level {
			end = j
			break
		}
	}
	at := last.index
	for j := last.index + 1; j < end; j++ {
		if strings.TrimSpace(lines[j]) != "" {
			at = j
		}
	}
	insert := []string{line}
	if at == last.index {
		insert = []string{"", line}
	}
	next := make([]string, 0, len(lines)+len(insert))
	next = append(next, lines[:at+1]...)
	next = append(next, insert...)
	next = append(next, lines[at+1:]...)
	out := strings.Join(next, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, last.text
}

func fencedLines(lines []string) []bool {
	out := make([]bool, len(lines))
	in := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			out[i] = true
			in = !in
			continue
		}
		out[i] = in
	}
	return out
}

func learningLine(date, text, key, href string) string {
	return "- " + date + ": " + text + ". Source: [" + linkLabel(key) + "](" + href + ")."
}

func linkLabel(key string) string {
	return strings.NewReplacer("[", "(", "]", ")", "(", "⟨", ")", "⟩").Replace(key)
}

// oneLine flattens text into the single changelog sentence. stripTag removes
// the process-learning token, which comments use and ticket titles do not.
func oneLine(s string, stripTag bool) string {
	if stripTag {
		s = processLearningToken.ReplaceAllString(s, "$1$2")
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.NewReplacer("`", "'", "[", "(", "]", ")", "<", "(", ">", ")").Replace(s)
	s = spaces.ReplaceAllString(s, " ")
	s = strings.Trim(s, " \t:;-–—#")
	s = strings.TrimRight(s, ". ")
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= 240 {
		return s
	}
	cut := 0
	for i := range s {
		if utf8.RuneCountInString(s[:i]) >= 239 {
			cut = i
			break
		}
	}
	if cut == 0 {
		return s
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

func commentLearningText(body string) (string, bool) {
	if !processLearningToken.MatchString(body) {
		return "", false
	}
	text := oneLine(body, true)
	if text == "" {
		return "", false
	}
	return text, true
}

func hasLearningTag(fields json.RawMessage) bool {
	var doc map[string]json.RawMessage
	if json.Unmarshal(fields, &doc) != nil {
		return false
	}
	raw, ok := doc["tags"]
	if !ok {
		return false
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return false
	}
	for _, item := range items {
		var name string
		if json.Unmarshal(item, &name) == nil {
			if learningTagName(name) {
				return true
			}
			continue
		}
		var obj struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(item, &obj) == nil && learningTagName(obj.Name) {
			return true
		}
	}
	return false
}

func learningTagName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), processLearningTag)
}

func learningHref(projectKey, nodeKey, kind, slug string) string {
	if s, ok := specFor(kind); ok && slug != "" {
		return "/p/" + url.PathEscape(projectKey) + "/knowledge/" + s.Type + "/" + url.PathEscape(slug)
	}
	if nodeKey != "" && !strings.EqualFold(nodeKey, projectKey) {
		return "/p/" + url.PathEscape(projectKey) + "/" + url.PathEscape(nodeKey)
	}
	return "/p/" + url.PathEscape(projectKey)
}

func nodeLearningID(nodeID string) string { return "n-" + nodeID }

func commentLearningID(nodeID, commentID string) string {
	return "c-" + nodeID + "-" + commentID
}

func parseLearningID(raw string) (nodeID, commentID string, comment bool, err error) {
	switch {
	case strings.HasPrefix(raw, "n-") && validUUID(raw[2:]):
		return raw[2:], "", false, nil
	case strings.HasPrefix(raw, "c-") && len(raw) > 39 && raw[38] == '-' && validUUID(raw[2:38]) && commentIDOK(raw[39:]):
		return raw[2:38], raw[39:], true, nil
	default:
		return "", "", false, fail(http.StatusBadRequest, "invalid_request", "invalid learning id")
	}
}

func commentIDOK(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---------- HTTP ----------

func (m *module) handleListLearnings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := r.URL.Query().Get("project_id")
	if !validUUID(project) {
		writeErr(w, fail(http.StatusBadRequest, "invalid_request", "project_id is required"))
		return
	}
	var page LearningPage
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		page, err = listLearnings(r.Context(), tx, p.TenantID, project)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (m *module) handleAcceptLearning(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := requirePerson(p); err != nil {
		writeErr(w, err)
		return
	}
	nodeID, commentID, comment, err := parseLearningID(r.PathValue("learningId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	raw, err := readObject(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	knowledgeID, err := parseAccept(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	expected, err := unmodifiedSince(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	decision, err := m.acceptLearning(r.Context(), p, r.PathValue("learningId"), nodeID, commentID, comment, knowledgeID, expected)
	if errors.Is(err, errStale) {
		writeErr(w, m.stale(r.Context(), p, knowledgeID))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decision)
}

func (m *module) handleDismissLearning(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := requirePerson(p); err != nil {
		writeErr(w, err)
		return
	}
	nodeID, commentID, comment, err := parseLearningID(r.PathValue("learningId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	decision, err := m.dismissLearning(r.Context(), p, r.PathValue("learningId"), nodeID, commentID, comment)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decision)
}

func parseAccept(raw map[string]json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fail(http.StatusBadRequest, "invalid_request", "knowledge_id is required")
	}
	for key := range raw {
		if key != "knowledge_id" {
			return "", fail(http.StatusBadRequest, "invalid_request", "unknown field "+key)
		}
	}
	id, ok, err := stringField(raw, "knowledge_id")
	if err != nil {
		return "", err
	}
	if !ok || !validUUID(strings.TrimSpace(id)) {
		return "", fail(http.StatusBadRequest, "invalid_request", "knowledge_id is required")
	}
	return strings.TrimSpace(id), nil
}

// ---------- List ----------

// owningProject is the node when it is a project, otherwise the closest project
// above it. nearestProject starts at the parent, so a comment on a nested
// project would otherwise belong to the project above that node.
const (
	owningProject    = `CASE WHEN k.slug='project' THEN n.id ELSE proj.id END`
	owningProjectKey = `CASE WHEN k.slug='project' THEN n.key ELSE proj.key END`
	// Decisions are dropped before the scan limit. A window of newer decided
	// items must not hide an older open learning or leave truncated false.
	undecidedNode = `NOT EXISTS (
	    SELECT 1 FROM method_learning_decisions d
	    WHERE d.tenant_id=n.tenant_id AND d.source_key='n-'||n.id::text)`
	undecidedComment = `NOT EXISTS (
	    SELECT 1 FROM method_learning_decisions d
	    WHERE d.tenant_id=c.tenant_id AND d.source_key='c-'||n.id::text||'-'||c.id::text)`
)

const projectTree = `
WITH RECURSIVE tree AS (
    SELECT id, 0 AS depth FROM nodes WHERE tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL
    UNION ALL
    SELECT n.id, tree.depth+1 FROM nodes n
    JOIN tree ON n.parent_id=tree.id
    WHERE n.tenant_id=$1 AND n.deleted_at IS NULL AND tree.depth<32
)`

func listLearnings(ctx context.Context, tx pgx.Tx, tenantID, projectID string) (LearningPage, error) {
	page := LearningPage{Items: []Learning{}}
	var projectKey string
	err := tx.QueryRow(ctx, `SELECT n.key FROM nodes n
	  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	  WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL AND k.slug='project'`, tenantID, projectID).Scan(&projectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return page, fail(http.StatusNotFound, "not_found", "project not found")
	}
	if err != nil {
		return page, err
	}
	decided, err := decidedKeys(ctx, tx, tenantID)
	if err != nil {
		return page, err
	}
	tickets, err := listTaggedIssues(ctx, tx, tenantID, projectID, projectKey, decided)
	if err != nil {
		return page, err
	}
	comments, err := listTaggedComments(ctx, tx, tenantID, projectID, projectKey, decided)
	if err != nil {
		return page, err
	}
	nominated, err := listNominated(ctx, tx, tenantID, projectID, projectKey, decided)
	if err != nil {
		return page, err
	}
	items := mergeNominations(append(tickets, comments...), nominated)
	sortLearnings(items)
	if len(items) > learningListLimit {
		items = items[:learningListLimit]
		page.Truncated = true
	}
	if items == nil {
		items = []Learning{}
	}
	page.Items = items
	return page, nil
}

func decidedKeys(ctx context.Context, tx pgx.Tx, tenantID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT source_key FROM method_learning_decisions WHERE tenant_id=$1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out[key] = true
	}
	return out, rows.Err()
}

func listTaggedIssues(ctx context.Context, tx pgx.Tx, tenantID, projectID, projectKey string, decided map[string]bool) ([]Learning, error) {
	rows, err := tx.Query(ctx, projectTree+`
	  SELECT n.id::text, n.key, n.title, n.updated_at, n.fields, k.slug, coalesce(n.fields->>'slug','')
	  FROM nodes n
	  JOIN tree ON tree.id=n.id
	  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	  `+nearestProject+`
	  WHERE n.tenant_id=$1 AND n.deleted_at IS NULL
	    AND k.slug IN ('ticket','task','epic')
	    AND proj.id=$2::uuid
	    AND n.fields::text ILIKE '%process-learning%'
	    AND `+undecidedNode+`
	  ORDER BY n.updated_at DESC
	  LIMIT `+strconv.Itoa(learningScanLimit), tenantID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Learning
	for rows.Next() {
		var item Learning
		var fields json.RawMessage
		var kind, slug string
		if err := rows.Scan(&item.NodeID, &item.Key, &item.Title, &item.At, &fields, &kind, &slug); err != nil {
			return nil, err
		}
		item.ID = nodeLearningID(item.NodeID)
		if decided[item.ID] || !issueKinds[kind] || !hasLearningTag(fields) {
			continue
		}
		item.Text = oneLine(item.Title, false)
		if item.Text == "" {
			continue
		}
		item.Source = "ticket"
		item.Href = learningHref(projectKey, item.Key, kind, slug)
		item.projectID = projectID
		out = append(out, item)
	}
	return out, rows.Err()
}

func listTaggedComments(ctx context.Context, tx pgx.Tx, tenantID, projectID, projectKey string, decided map[string]bool) ([]Learning, error) {
	rows, err := tx.Query(ctx, projectTree+`
	  SELECT c.id::text, c.at, n.id::text, n.key, n.title, k.slug, coalesce(n.fields->>'slug',''),
	         coalesce(latest.after->>'body_markdown', c.after->>'body_markdown', ''),
	         coalesce(target.id, person.id)::text, coalesce(target.name, person.name, '')
	  FROM events c
	  JOIN nodes n ON n.tenant_id=c.tenant_id AND n.id=c.node_id AND n.deleted_at IS NULL
	  JOIN tree ON tree.id=n.id
	  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	  `+nearestProject+`
	  JOIN principals person ON person.tenant_id=c.tenant_id AND person.id=c.actor_principal_id
	  LEFT JOIN principals target ON target.tenant_id=person.tenant_id AND target.id=person.linked_to
	  LEFT JOIN LATERAL (
	      SELECT after FROM events e
	      WHERE e.tenant_id=c.tenant_id AND e.node_id=c.node_id
	        AND e.type IN ('comment.updated','comment.deleted')
	        AND e.after->>'comment_id'=c.id::text
	      ORDER BY e.id DESC LIMIT 1
	  ) latest ON true
	  WHERE c.tenant_id=$1 AND c.type='comment.created'
	    AND `+owningProject+`=$2::uuid
	    AND coalesce(latest.after->>'deleted','false')<>'true'
	    AND coalesce(latest.after->>'body_markdown', c.after->>'body_markdown','') ILIKE '%process-learning%'
	    AND `+undecidedComment+`
	  ORDER BY c.at DESC
	  LIMIT `+strconv.Itoa(learningScanLimit), tenantID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Learning
	for rows.Next() {
		var item Learning
		var kind, slug, body, authorID, authorName string
		if err := rows.Scan(&item.CommentID, &item.At, &item.NodeID, &item.Key, &item.Title, &kind, &slug, &body, &authorID, &authorName); err != nil {
			return nil, err
		}
		text, ok := commentLearningText(body)
		item.ID = commentLearningID(item.NodeID, item.CommentID)
		if !ok || decided[item.ID] {
			continue
		}
		item.Source = "comment"
		item.Text = text
		item.Href = learningHref(projectKey, item.Key, kind, slug)
		item.projectID = projectID
		if authorID != "" && authorName != "" {
			item.Author = &Person{ID: authorID, Name: authorName}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func sortLearnings(items []Learning) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].At.Equal(items[j].At) {
			return items[i].ID > items[j].ID
		}
		return items[i].At.After(items[j].At)
	})
}

// ---------- Accept and dismiss ----------

func (m *module) acceptLearning(ctx context.Context, p tenant.Principal, publicID, nodeID, commentID string, comment bool, knowledgeID string, expected *time.Time) (LearningDecision, error) {
	var out LearningDecision
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if !canWrite(ctx, tx, p, "knowledge.write") {
			return fail(http.StatusForbidden, "forbidden", "you can read knowledge but not change it")
		}
		if err := lockLearning(ctx, tx, p.TenantID, publicID); err != nil {
			return err
		}
		if err := ensureUndecided(ctx, tx, p.TenantID, publicID); err != nil {
			return err
		}
		item, err := loadOpen(ctx, tx, p.TenantID, nodeID, commentID, comment)
		if err != nil {
			return err
		}
		current, _, err := lockNode(ctx, tx, p.TenantID, knowledgeID, false)
		if err != nil {
			return err
		}
		if expected != nil && !current.UpdatedAt.Equal(*expected) {
			return errStale
		}
		project, err := projectOf(ctx, tx, p.TenantID, current.ID)
		if err != nil {
			return err
		}
		if project == "" || project != item.projectID {
			return fail(http.StatusNotFound, "not_found", "knowledge entry not found")
		}
		var date string
		if err := tx.QueryRow(ctx, `SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD')`).Scan(&date); err != nil {
			return err
		}
		line := learningLine(date, item.Text, item.Key, item.Href)
		body, heading := appendChangelog(current.Body, line)
		updated, err := scanSnap(tx.QueryRow(ctx, `UPDATE nodes SET body=$3, updated_at=`+bumpUpdated+`
		  WHERE tenant_id=$1 AND id=$2::uuid RETURNING `+nodeReturning, p.TenantID, current.ID, body))
		if err != nil {
			return err
		}
		ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &updated.ID, Type: evLearningAccepted, Before: current, After: updated})
		if err != nil {
			return err
		}
		if err := insertDecision(ctx, tx, p, item.projectID, publicID, "accepted", &current.ID, &heading, &line, ev.ID, nil, nil, nil); err != nil {
			return err
		}
		entry, err := loadEntry(ctx, tx, p.TenantID, current.ID, false)
		if err != nil {
			return err
		}
		entry.EventID = &ev.ID
		out = LearningDecision{ID: publicID, Decision: "accepted", EventID: ev.ID, KnowledgeID: current.ID, Heading: heading, Line: line, Entry: &entry}
		return nil
	})
	return out, err
}

func (m *module) dismissLearning(ctx context.Context, p tenant.Principal, publicID, nodeID, commentID string, comment bool) (LearningDecision, error) {
	var out LearningDecision
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if !canWrite(ctx, tx, p, "knowledge.write") {
			return fail(http.StatusForbidden, "forbidden", "you can read knowledge but not change it")
		}
		if err := lockLearning(ctx, tx, p.TenantID, publicID); err != nil {
			return err
		}
		if err := ensureUndecided(ctx, tx, p.TenantID, publicID); err != nil {
			return err
		}
		item, err := loadOpen(ctx, tx, p.TenantID, nodeID, commentID, comment)
		if err != nil {
			return err
		}
		ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &item.NodeID, Type: evLearningDismissed, After: learningAudit{
			SourceKey: publicID, Source: item.Source, NodeID: item.NodeID, CommentID: item.CommentID, ProjectID: item.projectID, Text: item.Text,
		}})
		if err != nil {
			return err
		}
		if err := insertDecision(ctx, tx, p, item.projectID, publicID, "dismissed", nil, nil, nil, ev.ID, nil, nil, nil); err != nil {
			return err
		}
		out = LearningDecision{ID: publicID, Decision: "dismissed", EventID: ev.ID}
		return nil
	})
	return out, err
}

func lockLearning(ctx context.Context, tx pgx.Tx, tenantID, publicID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 275))`, tenantID+":learning:"+publicID)
	return err
}

func ensureUndecided(ctx context.Context, tx pgx.Tx, tenantID, publicID string) error {
	var decision string
	err := tx.QueryRow(ctx, `SELECT decision FROM method_learning_decisions WHERE tenant_id=$1 AND source_key=$2`, tenantID, publicID).Scan(&decision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return alreadyDecided()
}

func insertDecision(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, publicID, decision string, knowledgeID, heading, line *string, eventID int64, ruleSetID, ruleLayerID, ruleIdentity *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO method_learning_decisions
	  (tenant_id, project_id, source_key, decision, knowledge_id, heading, line, decided_by, event_id, rule_set_id, rule_layer_id, rule_identity)
	  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		p.TenantID, projectID, publicID, decision, knowledgeID, heading, line, p.ID, eventID, ruleSetID, ruleLayerID, ruleIdentity)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return alreadyDecided()
		}
	}
	return err
}

func loadOpen(ctx context.Context, tx pgx.Tx, tenantID, nodeID, commentID string, comment bool) (Learning, error) {
	var item Learning
	var kind, slug, projectKey, projectID string
	var fields json.RawMessage
	err := tx.QueryRow(ctx, `SELECT n.key, n.title, n.updated_at, n.fields, k.slug, coalesce(n.fields->>'slug',''),
	    coalesce(`+owningProjectKey+`, ''),
	    coalesce((`+owningProject+`)::text, '')
	  FROM nodes n
	  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	  `+nearestProject+`
	  WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL`, tenantID, nodeID).Scan(
		&item.Key, &item.Title, &item.At, &fields, &kind, &slug, &projectKey, &projectID)
	if errors.Is(err, pgx.ErrNoRows) || projectID == "" {
		return Learning{}, closedLearning()
	}
	if err != nil {
		return Learning{}, err
	}
	item.NodeID = nodeID
	item.projectID = projectID
	item.Href = learningHref(projectKey, item.Key, kind, slug)
	publicID := nodeLearningID(nodeID)
	if comment {
		publicID = commentLearningID(nodeID, commentID)
	}
	nom, err := nominationOf(ctx, tx, tenantID, publicID)
	if err != nil {
		return Learning{}, err
	}
	if comment {
		body, at, author, ok, err := openComment(ctx, tx, tenantID, nodeID, commentID)
		if err != nil || !ok {
			if err != nil {
				return Learning{}, err
			}
			return Learning{}, closedLearning()
		}
		text, tagged := commentLearningText(body)
		if nom != nil {
			switch nom.Origin {
			case "review_verdict":
				if !strings.Contains(body, "VERDICT") {
					return Learning{}, closedLearning()
				}
				if nom.Excerpt != "" {
					text = nom.Excerpt
				}
			case "incident_comment":
				if !incidentWord.MatchString(body) {
					return Learning{}, closedLearning()
				}
				if !tagged {
					text = oneLine(body, false)
				}
			default:
				if !tagged {
					return Learning{}, closedLearning()
				}
			}
		} else if !tagged {
			return Learning{}, closedLearning()
		}
		if text == "" {
			return Learning{}, closedLearning()
		}
		item.ID = publicID
		item.Source = "comment"
		item.CommentID = commentID
		item.Text = text
		item.At = at
		item.Author = author
		return item, nil
	}
	text := ""
	tagged := issueKinds[kind] && hasLearningTag(fields)
	if tagged {
		text = oneLine(item.Title, false)
	}
	if nom != nil && (nom.Origin == "closed_ticket" || nom.Origin == "review_verdict") {
		if !issueKinds[kind] {
			return Learning{}, closedLearning()
		}
		if nom.Origin == "review_verdict" && nom.Excerpt != "" {
			text = nom.Excerpt
		} else if text == "" {
			text = nom.Excerpt
		}
	} else if !tagged {
		return Learning{}, closedLearning()
	}
	if text == "" {
		return Learning{}, closedLearning()
	}
	item.ID = publicID
	item.Source = "ticket"
	item.Text = text
	return item, nil
}

type nomination struct {
	Origin    string
	Excerpt   string
	CommentID string
	NodeID    string
	At        time.Time
}

func nominationOf(ctx context.Context, tx pgx.Tx, tenantID, sourceKey string) (*nomination, error) {
	var nom nomination
	var commentID *string
	err := tx.QueryRow(ctx, `SELECT origin, excerpt, comment_id::text, node_id::text, nominated_at
		FROM method_learning_nominations WHERE tenant_id=$1 AND source_key=$2`, tenantID, sourceKey).Scan(&nom.Origin, &nom.Excerpt, &commentID, &nom.NodeID, &nom.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if commentID != nil {
		nom.CommentID = *commentID
	}
	return &nom, nil
}

func listNominated(ctx context.Context, tx pgx.Tx, tenantID, projectID, projectKey string, decided map[string]bool) ([]Learning, error) {
	rows, err := tx.Query(ctx, `SELECT source_key, node_id::text, comment_id::text, origin, excerpt, nominated_at
		FROM method_learning_nominations
		WHERE tenant_id=$1 AND project_id=$2::uuid
		ORDER BY nominated_at DESC
		LIMIT `+strconv.Itoa(learningScanLimit), tenantID, projectID)
	if err != nil {
		return nil, err
	}
	// Resolve each nomination after the scan. A query on this transaction
	// while rows are open is "conn busy".
	defer rows.Close()
	type nominatedHit struct {
		sourceKey, nodeID, origin, excerpt string
		commentID                          *string
		at                                 time.Time
	}
	var hits []nominatedHit
	for rows.Next() {
		var hit nominatedHit
		if err := rows.Scan(&hit.sourceKey, &hit.nodeID, &hit.commentID, &hit.origin, &hit.excerpt, &hit.at); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	var out []Learning
	for _, hit := range hits {
		if decided[hit.sourceKey] {
			continue
		}
		comment := ""
		if hit.commentID != nil {
			comment = *hit.commentID
		}
		item, ok, err := learningFromNomination(ctx, tx, tenantID, projectID, projectKey, hit.sourceKey, hit.nodeID, comment, hit.origin, hit.excerpt, hit.at)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func learningFromNomination(ctx context.Context, tx pgx.Tx, tenantID, projectID, projectKey, sourceKey, nodeID, commentID, origin, excerpt string, at time.Time) (Learning, bool, error) {
	var item Learning
	var kind, slug string
	err := tx.QueryRow(ctx, `SELECT n.key, n.title, n.updated_at, k.slug, coalesce(n.fields->>'slug','')
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL`, tenantID, nodeID).Scan(&item.Key, &item.Title, &item.At, &kind, &slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Learning{}, false, nil
	}
	if err != nil {
		return Learning{}, false, err
	}
	item.NodeID = nodeID
	item.ID = sourceKey
	item.Href = learningHref(projectKey, item.Key, kind, slug)
	item.projectID = projectID
	switch origin {
	case "closed_ticket":
		if !issueKinds[kind] {
			return Learning{}, false, nil
		}
		item.Source = "ticket"
		item.Text = oneLine(item.Title, false)
		if item.Text == "" {
			item.Text = excerpt
		}
	case "review_verdict":
		item.verdict = true
		item.Text = excerpt
		if commentID == "" {
			if !issueKinds[kind] {
				return Learning{}, false, nil
			}
			item.Source = "ticket"
		} else {
			body, commentAt, author, ok, err := openComment(ctx, tx, tenantID, nodeID, commentID)
			if err != nil || !ok || !strings.Contains(body, "VERDICT") {
				return Learning{}, false, err
			}
			item.Source = "comment"
			item.CommentID = commentID
			item.At = commentAt
			item.Author = author
		}
	case "incident_comment":
		body, commentAt, author, ok, err := openComment(ctx, tx, tenantID, nodeID, commentID)
		if err != nil || !ok || !incidentWord.MatchString(body) {
			return Learning{}, false, err
		}
		text, tagged := commentLearningText(body)
		if !tagged {
			text = oneLine(body, false)
		}
		if text == "" {
			return Learning{}, false, nil
		}
		item.Source = "comment"
		item.CommentID = commentID
		item.Text = text
		item.At = commentAt
		item.Author = author
	default:
		return Learning{}, false, nil
	}
	if item.Text == "" {
		return Learning{}, false, nil
	}
	if item.At.IsZero() {
		item.At = at
	}
	return item, true, nil
}

func mergeNominations(items, nominated []Learning) []Learning {
	index := map[string]int{}
	for i, item := range items {
		index[item.ID] = i
	}
	for _, nom := range nominated {
		if at, ok := index[nom.ID]; ok {
			if nom.verdict && nom.Text != "" {
				items[at].Text = nom.Text
			}
			continue
		}
		items = append(items, nom)
	}
	return items
}

func openComment(ctx context.Context, tx pgx.Tx, tenantID, nodeID, commentID string) (string, time.Time, *Person, bool, error) {
	var body, deleted, authorID, authorName string
	var at time.Time
	err := tx.QueryRow(ctx, `SELECT c.at,
	    coalesce(latest.after->>'body_markdown', c.after->>'body_markdown', ''),
	    coalesce(latest.after->>'deleted', 'false'),
	    coalesce(target.id, person.id)::text, coalesce(target.name, person.name, '')
	  FROM events c
	  JOIN principals person ON person.tenant_id=c.tenant_id AND person.id=c.actor_principal_id
	  LEFT JOIN principals target ON target.tenant_id=person.tenant_id AND target.id=person.linked_to
	  LEFT JOIN LATERAL (
	      SELECT after FROM events e
	      WHERE e.tenant_id=c.tenant_id AND e.node_id=c.node_id
	        AND e.type IN ('comment.updated','comment.deleted')
	        AND e.after->>'comment_id'=$3
	      ORDER BY e.id DESC LIMIT 1
	  ) latest ON true
	  WHERE c.tenant_id=$1 AND c.node_id=$2::uuid AND c.id=$4::bigint AND c.type='comment.created'`,
		tenantID, nodeID, commentID, commentID).Scan(&at, &body, &deleted, &authorID, &authorName)
	if errors.Is(err, pgx.ErrNoRows) || deleted == "true" {
		return "", time.Time{}, nil, false, nil
	}
	if err != nil {
		return "", time.Time{}, nil, false, err
	}
	var author *Person
	if authorID != "" && authorName != "" {
		author = &Person{ID: authorID, Name: authorName}
	}
	return body, at, author, true, nil
}

// ---------- Undo ----------

func undoLearningAccepted(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if p.Kind != tenant.Person {
		return events.Change{}, events.ErrForbidden
	}
	before, _, current, err := guard(ctx, tx, p, e, false)
	if err != nil {
		return events.Change{}, err
	}
	if before.ID == "" {
		return events.Change{}, events.ErrConflict
	}
	tag, err := tx.Exec(ctx, `DELETE FROM method_learning_decisions WHERE tenant_id=$1 AND event_id=$2`, p.TenantID, e.ID)
	if err != nil {
		return events.Change{}, err
	}
	if tag.RowsAffected() != 1 {
		return events.Change{}, events.ErrConflict
	}
	restored, err := scanSnap(tx.QueryRow(ctx, `UPDATE nodes SET title=$3, body=$4, state=$5, fields=$6::jsonb, updated_at=`+bumpUpdated+`
	  WHERE tenant_id=$1 AND id=$2::uuid RETURNING `+nodeReturning, p.TenantID, current.ID, before.Title, before.Body, before.State, string(before.Fields)))
	if err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: e.NodeID, Type: evLearningAccepted, Before: current, After: restored}, nil
}

func undoLearningDismissed(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if p.Kind != tenant.Person {
		return events.Change{}, events.ErrForbidden
	}
	if !canWrite(ctx, tx, p, "knowledge.write") {
		return events.Change{}, events.ErrForbidden
	}
	tag, err := tx.Exec(ctx, `DELETE FROM method_learning_decisions WHERE tenant_id=$1 AND event_id=$2`, p.TenantID, e.ID)
	if err != nil {
		return events.Change{}, err
	}
	if tag.RowsAffected() != 1 {
		return events.Change{}, events.ErrConflict
	}
	return events.Change{NodeID: e.NodeID, Type: evLearningDismissed, Before: json.RawMessage(e.After), After: map[string]any{"decision": "reopened"}}, nil
}
