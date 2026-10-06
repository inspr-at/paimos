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

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/markdownsource"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
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
	issueKinds           = map[string]bool{"work": true, "ticket": true, "task": true, "epic": true}
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
	// Recommendation is an agent's (or a person's) prepared decision
	// (AEON-788). It never decides anything by itself.
	Recommendation *Recommendation `json:"recommendation,omitempty"`
	projectID      string
	verdict        bool
}

// LearningPage is GET /api/knowledge/learnings.
// Truncated means more open learnings follow; NextCursor then continues the
// list after the last item (AEON-788).
type LearningPage struct {
	Items      []Learning `json:"items"`
	Truncated  bool       `json:"truncated"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// learningAudit is the dismiss event. node_id and project_id are real nodes.
type learningAudit struct {
	SourceKey string `json:"source_key"`
	Source    string `json:"source"`
	NodeID    string `json:"node_id"`
	CommentID string `json:"comment_id,omitempty"`
	ProjectID string `json:"project_id"`
	Text      string `json:"text"`
	Reason    string `json:"reason,omitempty"`
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

// sensitiveLearning refuses to copy text that looks like it holds a
// credential into a changelog or a rule draft. It names the matched ranges,
// never the text. A person may repeat the request with
// confirm_not_sensitive; the event records that they did.
func sensitiveLearning(ranges []SensitiveRange) error {
	e := fail(http.StatusConflict, "learning_sensitive", "This looks like a credential — remove it, or confirm it is not one.")
	e.ranges = ranges
	return e
}

// sensitiveCheck returns the refusal for flagged text, or whether a person's
// confirmation overrode a match.
func sensitiveCheck(ranges []SensitiveRange, confirm bool) (bool, error) {
	if len(ranges) == 0 {
		return false, nil
	}
	if !confirm {
		return false, sensitiveLearning(ranges)
	}
	return true, nil
}

// acceptedSnap is the learning_accepted after-image: the entry snapshot, and
// whether a person confirmed that a suspected credential is not one. Undo
// reads the snapshot and ignores the flag.
type acceptedSnap struct {
	nodeSnap
	ConfirmedNotSensitive bool `json:"confirmed_not_sensitive,omitempty"`
}

func alreadyDecided() error {
	return fail(http.StatusConflict, "already_decided", "This learning was already accepted or dismissed.")
}

// changedLearning refuses a decision on text the person did not review: the
// learning reads differently now than when they sent it (AEON-788).
func changedLearning() error {
	return fail(http.StatusConflict, "learning_changed", "This learning changed since you reviewed it. Review it again.")
}

// reviewedMatches is whether the text the person reviewed is the learning's
// text now. Older clients send none and are not checked.
func reviewedMatches(reviewed *string, current string) bool {
	return reviewed == nil || *reviewed == current
}

// fenceLearningWrite holds project placement and access steady through
// commit (tenant, then tree, before any learning or node lock), then
// authorizes against the source's current project. The route's scope was
// resolved before this transaction; the item may have moved, or the grant
// been revoked, since then (AEON-788).
func fenceLearningWrite(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID string) error {
	if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
		return err
	}
	forbidden := fail(http.StatusForbidden, "forbidden", "you can read knowledge but not change it")
	if !canWrite(ctx, tx, p, "knowledge.write") {
		return forbidden
	}
	var project *string
	err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL`, p.TenantID, nodeID).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return closedLearning()
	}
	if err != nil {
		return err
	}
	scope := authz.Scope{}
	if project != nil {
		scope.ProjectID = *project
	}
	if authz.RequireTx(ctx, tx, p, "knowledge.write", scope) != nil {
		return forbidden
	}
	return nil
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
	return markdownsource.CodeLines(strings.Join(lines, "\n"))
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
	cursor, err := parseLearningCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var page LearningPage
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		page, err = listLearningPage(r.Context(), tx, p.TenantID, project, cursor)
		if err != nil {
			return err
		}
		return attachRecommendations(r.Context(), tx, p.TenantID, project, page.Items)
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
	in, err := parseAccept(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	expected, err := unmodifiedSince(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	decision, err := m.acceptLearning(r.Context(), p, r.PathValue("learningId"), nodeID, commentID, comment, in, expected)
	if errors.Is(err, errStale) {
		writeErr(w, m.stale(r.Context(), p, in.KnowledgeID))
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
	reason, reviewed, err := readDismiss(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	decision, err := m.dismissLearning(r.Context(), p, r.PathValue("learningId"), nodeID, commentID, comment, reason, reviewed)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decision)
}

type acceptInput struct {
	KnowledgeID string
	Lesson      string
	Reviewed    *string
	Confirm     bool
}

// parseAccept reads knowledge_id, an optional lesson that replaces the
// learning's own text in the changelog line and the learning_text the person
// reviewed (AEON-788), and confirm_not_sensitive.
func parseAccept(raw map[string]json.RawMessage) (acceptInput, error) {
	var in acceptInput
	if len(raw) == 0 {
		return in, fail(http.StatusBadRequest, "invalid_request", "knowledge_id is required")
	}
	for key := range raw {
		switch key {
		case "knowledge_id", "confirm_not_sensitive", "lesson", "learning_text":
		default:
			return in, fail(http.StatusBadRequest, "invalid_request", "unknown field "+key)
		}
	}
	id, ok, err := stringField(raw, "knowledge_id")
	if err != nil {
		return in, err
	}
	if !ok || !validUUID(strings.TrimSpace(id)) {
		return in, fail(http.StatusBadRequest, "invalid_request", "knowledge_id is required")
	}
	in.KnowledgeID = strings.TrimSpace(id)
	if in.Lesson, err = lessonField(raw); err != nil {
		return in, err
	}
	if in.Reviewed, err = reviewedField(raw); err != nil {
		return in, err
	}
	if in.Confirm, err = confirmField(raw); err != nil {
		return in, err
	}
	return in, nil
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
	return listLearningPage(ctx, tx, tenantID, projectID, nil)
}

// listLearningPage is one page of 50, newest first, continuing after cursor
// when one is given. Tickets, comments and nominations each scan in the
// list's own order, (at, id) newest first, from the full cursor key, so ties
// and older learnings stay reachable beyond each scan's limit. A scan that
// fills its limit has unseen rows after its last one; the page ends there
// (its horizon), so no learning from another source beyond it is listed
// ahead of them.
func listLearningPage(ctx context.Context, tx pgx.Tx, tenantID, projectID string, cursor *learningCursor) (LearningPage, error) {
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
	tickets, ticketsEnd, err := listTaggedIssues(ctx, tx, tenantID, projectID, projectKey, decided, cursor)
	if err != nil {
		return page, err
	}
	comments, commentsEnd, err := listTaggedComments(ctx, tx, tenantID, projectID, projectKey, decided, cursor)
	if err != nil {
		return page, err
	}
	nominated, nominatedEnd, err := listNominated(ctx, tx, tenantID, projectID, projectKey, cursor)
	if err != nil {
		return page, err
	}
	items := mergeNominations(append(tickets, comments...), nominated)
	sortLearnings(items)
	if cursor != nil {
		items = afterCursor(items, *cursor)
	}
	var horizon *learningCursor
	for _, end := range []*learningCursor{ticketsEnd, commentsEnd, nominatedEnd} {
		if end != nil && (horizon == nil || end.sortsBefore(*horizon)) {
			horizon = end
		}
	}
	if horizon != nil {
		kept := items[:0]
		for _, item := range items {
			if !horizon.sortsBefore(learningCursor{At: item.At, ID: item.ID}) {
				kept = append(kept, item)
			}
		}
		items = kept
		page.Truncated = true
		page.NextCursor = horizon.encode()
	}
	if len(items) > learningListLimit {
		items = items[:learningListLimit]
		page.Truncated = true
		page.NextCursor = encodeLearningCursor(items[len(items)-1])
	}
	if items == nil {
		items = []Learning{}
	}
	page.Items = items
	return page, nil
}

// AnalysisLearnings gives the system outcome job the same current inbox as the
// person view, including edits, deletions, nominations and decisions. Callers
// must establish tenant and project visibility in the supplied transaction.
func AnalysisLearnings(ctx context.Context, tx pgx.Tx, tenantID, projectID string) (doctrine.AnalysisLearningPage, error) {
	page, err := listLearnings(ctx, tx, tenantID, projectID)
	out := doctrine.AnalysisLearningPage{Truncated: page.Truncated}
	for _, item := range page.Items {
		out.Items = append(out.Items, doctrine.AnalysisLearning{ID: item.ID, NodeID: item.NodeID, Key: item.Key, Href: item.Href, Text: item.Text, At: item.At})
	}
	return out, err
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

// scanEnd is the key of a scan's last row when the scan filled its limit:
// rows after it were not read. nil when the scan reached its end.
func scanEnd(rows int, last learningCursor) *learningCursor {
	if rows < learningScanLimit {
		return nil
	}
	return &last
}

// cursorArgs are the $3 timestamp and $4 id of an optional cursor.
func cursorArgs(c *learningCursor) (*time.Time, string) {
	if c == nil {
		return nil, ""
	}
	return &c.At, c.ID
}

func listTaggedIssues(ctx context.Context, tx pgx.Tx, tenantID, projectID, projectKey string, decided map[string]bool, cursor *learningCursor) ([]Learning, *learningCursor, error) {
	before, beforeID := cursorArgs(cursor)
	rows, err := tx.Query(ctx, projectTree+`
	  SELECT n.id::text, n.key, n.title, n.updated_at, n.fields, k.slug, coalesce(n.fields->>'slug','')
	  FROM nodes n
	  JOIN tree ON tree.id=n.id
	  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	  `+nearestProject+`
	  WHERE n.tenant_id=$1 AND n.deleted_at IS NULL
	    AND k.slug IN ('work','ticket','task','epic')
	    AND proj.id=$2::uuid
	    AND n.fields::text ILIKE '%process-learning%'
	    AND `+undecidedNode+`
	    AND ($3::timestamptz IS NULL OR n.updated_at < $3
	         OR (n.updated_at = $3 AND ('n-'||n.id::text) COLLATE "C" < $4::text COLLATE "C"))
	  ORDER BY n.updated_at DESC, ('n-'||n.id::text) COLLATE "C" DESC
	  LIMIT `+strconv.Itoa(learningScanLimit), tenantID, projectID, before, beforeID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []Learning
	var scanned int
	var last learningCursor
	for rows.Next() {
		var item Learning
		var fields json.RawMessage
		var kind, slug string
		if err := rows.Scan(&item.NodeID, &item.Key, &item.Title, &item.At, &fields, &kind, &slug); err != nil {
			return nil, nil, err
		}
		item.ID = nodeLearningID(item.NodeID)
		scanned, last = scanned+1, learningCursor{At: item.At, ID: item.ID}
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
	return out, scanEnd(scanned, last), rows.Err()
}

func listTaggedComments(ctx context.Context, tx pgx.Tx, tenantID, projectID, projectKey string, decided map[string]bool, cursor *learningCursor) ([]Learning, *learningCursor, error) {
	before, beforeID := cursorArgs(cursor)
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
	    AND ($3::timestamptz IS NULL OR c.at < $3
	         OR (c.at = $3 AND ('c-'||n.id::text||'-'||c.id::text) COLLATE "C" < $4::text COLLATE "C"))
	  ORDER BY c.at DESC, ('c-'||n.id::text||'-'||c.id::text) COLLATE "C" DESC
	  LIMIT `+strconv.Itoa(learningScanLimit), tenantID, projectID, before, beforeID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []Learning
	var scanned int
	var last learningCursor
	for rows.Next() {
		var item Learning
		var kind, slug, body, authorID, authorName string
		if err := rows.Scan(&item.CommentID, &item.At, &item.NodeID, &item.Key, &item.Title, &kind, &slug, &body, &authorID, &authorName); err != nil {
			return nil, nil, err
		}
		text, ok := commentLearningText(body)
		item.ID = commentLearningID(item.NodeID, item.CommentID)
		scanned, last = scanned+1, learningCursor{At: item.At, ID: item.ID}
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
	return out, scanEnd(scanned, last), rows.Err()
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

func (m *module) acceptLearning(ctx context.Context, p tenant.Principal, publicID, nodeID, commentID string, comment bool, in acceptInput, expected *time.Time) (LearningDecision, error) {
	var out LearningDecision
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := fenceLearningWrite(ctx, tx, p, nodeID); err != nil {
			return err
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
		if !reviewedMatches(in.Reviewed, item.Text) {
			return changedLearning()
		}
		if in.Lesson != "" {
			item.Text = in.Lesson
		}
		confirmed, err := sensitiveCheck(sensitiveRanges("text", item.Text), in.Confirm)
		if err != nil {
			return err
		}
		current, _, err := lockNode(ctx, tx, p.TenantID, in.KnowledgeID, false)
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
		ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &updated.ID, Type: evLearningAccepted, Before: current, After: acceptedSnap{updated, confirmed}})
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

func (m *module) dismissLearning(ctx context.Context, p tenant.Principal, publicID, nodeID, commentID string, comment bool, reason string, reviewed *string) (LearningDecision, error) {
	var out LearningDecision
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := fenceLearningWrite(ctx, tx, p, nodeID); err != nil {
			return err
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
		if !reviewedMatches(reviewed, item.Text) {
			return changedLearning()
		}
		if looksSensitive(reason) {
			return sensitiveLearning(sensitiveRanges("reason", reason))
		}
		ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &item.NodeID, Type: evLearningDismissed, After: learningAudit{
			SourceKey: publicID, Source: item.Source, NodeID: item.NodeID, CommentID: item.CommentID, ProjectID: item.projectID, Text: item.Text, Reason: reason,
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
	var kind, slug, projectKey, projectID, rootProject string
	var fields json.RawMessage
	err := tx.QueryRow(ctx, `SELECT n.key, n.title, n.updated_at, n.fields, k.slug, coalesce(n.fields->>'slug',''),
	    coalesce(`+owningProjectKey+`, ''),
	    coalesce((`+owningProject+`)::text, ''),
	    coalesce(n.project_id::text, '')
	  FROM nodes n
	  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	  `+nearestProject+`
	  WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL`, tenantID, nodeID).Scan(
		&item.Key, &item.Title, &item.At, &fields, &kind, &slug, &projectKey, &projectID, &rootProject)
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
			nomText, ok, err := nominationText(ctx, tx, tenantID, *nom, item.Title, kind, rootProject, &body)
			if err != nil {
				return Learning{}, err
			}
			switch {
			case ok && (nom.Origin == "review_verdict" || !tagged):
				text = nomText
			case !ok && !tagged:
				return Learning{}, closedLearning()
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
	if issueKinds[kind] && hasLearningTag(fields) {
		text = oneLine(item.Title, false)
	}
	if nom != nil && (nom.Origin == "closed_ticket" || nom.Origin == "review_verdict") {
		nomText, ok, err := nominationText(ctx, tx, tenantID, *nom, item.Title, kind, rootProject, nil)
		if err != nil {
			return Learning{}, err
		}
		if ok && (nom.Origin == "review_verdict" || text == "") {
			text = nomText
		}
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
	Origin     string
	Excerpt    string
	SourceHash string
	CommentID  string
	NodeID     string
	ProjectID  string
	OutcomeID  *string
	At         time.Time
}

const nominationColumns = `origin, excerpt, source_hash, coalesce(comment_id::text, ''), node_id::text, project_id::text, outcome_id, nominated_at`

func scanNomination(row pgx.Row, extra ...any) (nomination, error) {
	var nom nomination
	dest := append([]any{&nom.Origin, &nom.Excerpt, &nom.SourceHash, &nom.CommentID, &nom.NodeID, &nom.ProjectID, &nom.OutcomeID, &nom.At}, extra...)
	return nom, row.Scan(dest...)
}

func nominationOf(ctx context.Context, tx pgx.Tx, tenantID, sourceKey string) (*nomination, error) {
	nom, err := scanNomination(tx.QueryRow(ctx, `SELECT `+nominationColumns+`
		FROM method_learning_nominations WHERE tenant_id=$1 AND source_key=$2`, tenantID, sourceKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &nom, nil
}

// nominationText is a nomination's inbox text, re-derived from its live
// source: the ticket title, the comment as it reads now, or the review
// verdict record. The stored excerpt is served only while the source hashes
// as it did when it was nominated; an edited source gets the new text, and a
// source that now looks like it holds a credential hides the nomination.
//
// A review verdict counts only in the project it was recorded in, while the
// ticket is still there, and only when the caller can read the verdict
// record itself: a ticket moved from A to B does not carry A's verdict to
// people who see only B. body is the live comment for a comment nomination.
func nominationText(ctx context.Context, tx pgx.Tx, tenantID string, nom nomination, title, kind, nodeProject string, body *string) (string, bool, error) {
	var excerpt, material string
	switch {
	case nom.Origin == "closed_ticket":
		if !issueKinds[kind] {
			return "", false, nil
		}
		excerpt, material = oneLine(title, false), title
	case nom.Origin == "review_verdict" && nom.CommentID != "":
		if body == nil || !strings.Contains(*body, "VERDICT") {
			return "", false, nil
		}
		excerpt, material = oneLine(*body, false), *body
	case nom.Origin == "review_verdict":
		if nom.OutcomeID == nil || !issueKinds[kind] || nom.ProjectID != nodeProject {
			return "", false, nil
		}
		payload, ok, err := outcomePayload(ctx, tx, tenantID, *nom.OutcomeID)
		if err != nil || !ok {
			return "", false, err
		}
		if excerpt, material, ok = verdictText(payload, title); !ok {
			return "", false, nil
		}
	case nom.Origin == "incident_comment":
		if body == nil || !incidentWord.MatchString(*body) {
			return "", false, nil
		}
		excerpt, material = oneLine(*body, false), *body
	default:
		return "", false, nil
	}
	if looksSensitive(material) {
		return "", false, nil
	}
	if nom.Excerpt != "" && nom.SourceHash == sourceHash(material) {
		excerpt = nom.Excerpt
	}
	return excerpt, excerpt != "", nil
}

// outcomePayload reads one review verdict with the caller's own visibility,
// so a verdict in a project the caller cannot see is not found. A missing
// outcome table is not found either.
func outcomePayload(ctx context.Context, tx pgx.Tx, tenantID, outcomeID string) (json.RawMessage, bool, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	var payload json.RawMessage
	err = sp.QueryRow(ctx, `SELECT payload FROM outcome_events
		WHERE tenant_id=$1 AND id::text=$2 AND kind='review_verdict'`, tenantID, outcomeID).Scan(&payload)
	if err != nil {
		_ = sp.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) || missingOutcomeSchema(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return payload, true, sp.Commit(ctx)
}

// listNominated drops decided nominations in SQL, before the limit, so a
// window of newer decided ones cannot hide an older open candidate. It scans
// in the list's order: a nomination's time is its comment's, or its node's
// updated_at, as learningFromNomination lists it; the id is the source key.
func listNominated(ctx context.Context, tx pgx.Tx, tenantID, projectID, projectKey string, cursor *learningCursor) ([]Learning, *learningCursor, error) {
	before, beforeID := cursorArgs(cursor)
	// nominationColumns, qualified: nodes and events share their names.
	rows, err := tx.Query(ctx, `SELECT m.origin, m.excerpt, m.source_hash, coalesce(m.comment_id::text, ''), m.node_id::text,
		  m.project_id::text, m.outcome_id, m.nominated_at, m.source_key, x.at
		FROM method_learning_nominations m
		JOIN nodes n ON n.tenant_id=m.tenant_id AND n.id=m.node_id AND n.deleted_at IS NULL
		LEFT JOIN events c ON c.tenant_id=m.tenant_id AND c.id=m.comment_id AND c.node_id=m.node_id AND c.type='comment.created'
		CROSS JOIN LATERAL (SELECT CASE WHEN m.comment_id IS NULL THEN n.updated_at ELSE c.at END AS at) x
		WHERE m.tenant_id=$1 AND m.project_id=$2::uuid
		  AND x.at IS NOT NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM method_learning_decisions d
		    WHERE d.tenant_id=m.tenant_id AND d.source_key=m.source_key)
		  AND ($3::timestamptz IS NULL OR x.at < $3
		       OR (x.at = $3 AND m.source_key COLLATE "C" < $4::text COLLATE "C"))
		ORDER BY x.at DESC, m.source_key COLLATE "C" DESC
		LIMIT `+strconv.Itoa(learningScanLimit), tenantID, projectID, before, beforeID)
	if err != nil {
		return nil, nil, err
	}
	// Resolve each nomination after the scan. A query on this transaction
	// while rows are open is "conn busy".
	defer rows.Close()
	type nominatedHit struct {
		sourceKey string
		nom       nomination
	}
	var hits []nominatedHit
	var last learningCursor
	for rows.Next() {
		var hit nominatedHit
		var sourceKey string
		var at time.Time
		nom, err := scanNomination(rows, &sourceKey, &at)
		if err != nil {
			return nil, nil, err
		}
		hit.sourceKey, hit.nom = sourceKey, nom
		hits = append(hits, hit)
		last = learningCursor{At: at, ID: sourceKey}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows.Close()
	var out []Learning
	for _, hit := range hits {
		item, ok, err := learningFromNomination(ctx, tx, tenantID, projectID, projectKey, hit.sourceKey, hit.nom)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			out = append(out, item)
		}
	}
	return out, scanEnd(len(hits), last), nil
}

func learningFromNomination(ctx context.Context, tx pgx.Tx, tenantID, projectID, projectKey, sourceKey string, nom nomination) (Learning, bool, error) {
	var item Learning
	var kind, slug, nodeProject string
	err := tx.QueryRow(ctx, `SELECT n.key, n.title, n.updated_at, k.slug, coalesce(n.fields->>'slug',''), coalesce(n.project_id::text, '')
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL`, tenantID, nom.NodeID).Scan(&item.Key, &item.Title, &item.At, &kind, &slug, &nodeProject)
	if errors.Is(err, pgx.ErrNoRows) {
		return Learning{}, false, nil
	}
	if err != nil {
		return Learning{}, false, err
	}
	item.NodeID = nom.NodeID
	item.ID = sourceKey
	item.Href = learningHref(projectKey, item.Key, kind, slug)
	item.projectID = projectID
	var body *string
	if nom.CommentID != "" {
		text, commentAt, author, ok, err := openComment(ctx, tx, tenantID, nom.NodeID, nom.CommentID)
		if err != nil || !ok {
			return Learning{}, false, err
		}
		body = &text
		item.Source = "comment"
		item.CommentID = nom.CommentID
		item.At = commentAt
		item.Author = author
	} else {
		item.Source = "ticket"
	}
	text, ok, err := nominationText(ctx, tx, tenantID, nom, item.Title, kind, nodeProject, body)
	if err != nil || !ok {
		return Learning{}, false, err
	}
	switch nom.Origin {
	case "review_verdict":
		item.verdict = true
		item.Text = text
	case "incident_comment":
		if tagged, isTagged := commentLearningText(*body); isTagged {
			item.Text = tagged
		} else {
			item.Text = text
		}
	default:
		item.Text = text
	}
	if item.Text == "" {
		return Learning{}, false, nil
	}
	if item.At.IsZero() {
		item.At = nom.At
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
