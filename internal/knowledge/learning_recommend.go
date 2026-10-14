// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Agents prepare method-learning decisions; people make them (AEON-788).
// A recommendation is accept (a lesson line and a target entry) or dismiss
// (a reason). It is stored once per learning, replaced in place, recorded as
// knowledge.learning_recommended, and never applies anything: a person
// applies it through accept or draft. Agents may dismiss noise with a reason.
const (
	evLearningRecommended = "knowledge.learning_recommended"
	maxLessonRunes        = 240
	maxReasonRunes        = 500
	maxCursorBytes        = 160
	maxDismissBody        = 8 << 10
	maxReviewedRunes      = 2000
)

// Recommendation is the current prepared decision for one open learning.
// Stale means the learning's text changed after it was recommended.
// TargetMissing means the chosen entry is gone or left the project.
type Recommendation struct {
	Decision       string    `json:"decision"`
	KnowledgeID    string    `json:"knowledge_id,omitempty"`
	KnowledgeTitle string    `json:"knowledge_title,omitempty"`
	SourceSummary  string    `json:"source_summary,omitempty"`
	Line           string    `json:"line,omitempty"`
	RuleLayerID    string    `json:"rule_layer_id,omitempty"`
	RuleText       string    `json:"rule_text,omitempty"`
	Prepared       bool      `json:"prepared"`
	Lesson         string    `json:"lesson,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	By             *Person   `json:"by"`
	At             time.Time `json:"at"`
	EventID        int64     `json:"event_id"`
	Stale          bool      `json:"stale"`
	TargetMissing  bool      `json:"target_missing"`
}

// recommendationAudit is the learning_recommended after-image.
type recommendationAudit struct {
	SourceKey     string `json:"source_key"`
	ProjectID     string `json:"project_id"`
	NodeID        string `json:"node_id"`
	CommentID     string `json:"comment_id,omitempty"`
	Decision      string `json:"decision"`
	KnowledgeID   string `json:"knowledge_id,omitempty"`
	Lesson        string `json:"lesson,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Text          string `json:"text"`
	SourceSummary string `json:"source_summary,omitempty"`
	Line          string `json:"line,omitempty"`
	RuleLayerID   string `json:"rule_layer_id,omitempty"`
}

type recommendationInput struct {
	Decision      string
	KnowledgeID   string
	Lesson        string
	Reason        string
	SourceSummary string
	RuleLayerID   string
}

// ---------- Text bounds ----------

// lessonField reads an optional lesson: one changelog sentence of at most 240
// characters. Absent or blank is "".
func lessonField(raw map[string]json.RawMessage) (string, error) {
	text, ok, err := stringField(raw, "lesson")
	if err != nil || !ok {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	if utf8.RuneCountInString(text) > maxLessonRunes {
		return "", fail(http.StatusBadRequest, "invalid_request", "lesson is longer than 240 characters")
	}
	line := oneLine(text, false)
	if line == "" {
		return "", fail(http.StatusBadRequest, "invalid_request", "lesson is empty")
	}
	return line, nil
}

// plainReason is a reason as one line of plain text, at most 500 characters.
func plainReason(text string) (string, error) {
	if utf8.RuneCountInString(text) > maxReasonRunes*2 {
		return "", fail(http.StatusBadRequest, "invalid_request", "reason is longer than 500 characters")
	}
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > maxReasonRunes {
		return "", fail(http.StatusBadRequest, "invalid_request", "reason is longer than 500 characters")
	}
	return text, nil
}

// reviewedField reads the optional learning_text: the learning's text as the
// person reviewed it. nil when absent.
func reviewedField(raw map[string]json.RawMessage) (*string, error) {
	text, ok, err := stringField(raw, "learning_text")
	if err != nil || !ok {
		return nil, err
	}
	if utf8.RuneCountInString(text) > maxReviewedRunes {
		return nil, fail(http.StatusBadRequest, "invalid_request", "learning_text is longer than 2000 characters")
	}
	return &text, nil
}

// readDismiss reads the optional {"reason": "…", "learning_text": "…"} of a
// dismiss. Older clients send no body or {}; both still dismiss without a
// reason and without a review check.
func readDismiss(w http.ResponseWriter, r *http.Request) (string, *string, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDismissBody))
	if err != nil {
		return "", nil, fail(http.StatusRequestEntityTooLarge, "invalid_request", "request body is too large")
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return "", nil, nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil || raw == nil {
		return "", nil, nil
	}
	reviewed, err := reviewedField(raw)
	if err != nil {
		return "", nil, err
	}
	reason, ok, err := stringField(raw, "reason")
	if err != nil || !ok {
		return "", reviewed, err
	}
	reason, err = plainReason(reason)
	return reason, reviewed, err
}

func parseRecommendation(raw map[string]json.RawMessage) (recommendationInput, error) {
	var in recommendationInput
	for key := range raw {
		switch key {
		case "decision", "knowledge_id", "lesson", "reason", "source_summary", "rule_layer_id":
		default:
			return in, fail(http.StatusBadRequest, "invalid_request", "unknown field "+key)
		}
	}
	decision, _, err := stringField(raw, "decision")
	if err != nil {
		return in, err
	}
	in.Decision = strings.TrimSpace(decision)
	reason, _, err := stringField(raw, "reason")
	if err != nil {
		return in, err
	}
	if in.Reason, err = plainReason(reason); err != nil {
		return in, err
	}
	summary, _, err := stringField(raw, "source_summary")
	if err != nil {
		return in, err
	}
	if in.SourceSummary, err = plainReason(summary); err != nil {
		return in, fail(http.StatusBadRequest, "invalid_request", "source_summary is longer than 500 characters")
	}
	layer, hasLayer, err := stringField(raw, "rule_layer_id")
	if err != nil {
		return in, err
	}
	if hasLayer {
		if in.Decision != "accept" || !validUUID(layer) {
			return in, fail(http.StatusBadRequest, "invalid_request", "rule_layer_id requires an accept recommendation and a UUID")
		}
		in.RuleLayerID = strings.ToLower(layer)
	}
	switch in.Decision {
	case "accept":
		id, _, err := stringField(raw, "knowledge_id")
		if err != nil {
			return in, err
		}
		if !validUUID(strings.TrimSpace(id)) {
			return in, fail(http.StatusBadRequest, "invalid_request", "knowledge_id is required to recommend accept")
		}
		in.KnowledgeID = strings.ToLower(strings.TrimSpace(id))
		if in.Lesson, err = lessonField(raw); err != nil {
			return in, err
		}
	case "dismiss":
		if _, ok := raw["knowledge_id"]; ok {
			return in, fail(http.StatusBadRequest, "invalid_request", "a dismiss recommendation has no knowledge_id")
		}
		if _, ok := raw["lesson"]; ok {
			return in, fail(http.StatusBadRequest, "invalid_request", "a dismiss recommendation has no lesson")
		}
		if in.Reason == "" {
			return in, fail(http.StatusBadRequest, "invalid_request", "reason is required to recommend dismiss")
		}
	default:
		return in, fail(http.StatusBadRequest, "invalid_request", "decision must be accept or dismiss")
	}
	return in, nil
}

// ---------- Continuation ----------

// learningCursor is the key the previous page ended at: the next page holds
// the items after it in newest-first order (older, or as old with a smaller
// id, by byte order as SQL's COLLATE "C").
type learningCursor struct {
	At time.Time
	ID string
}

// sortsBefore is whether c comes before o in the list's newest-first order.
func (c learningCursor) sortsBefore(o learningCursor) bool {
	if c.At.Equal(o.At) {
		return c.ID > o.ID
	}
	return c.At.After(o.At)
}

func (c learningCursor) encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.At.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

func encodeLearningCursor(item Learning) string {
	return learningCursor{At: item.At, ID: item.ID}.encode()
}

func parseLearningCursor(raw string) (*learningCursor, error) {
	if raw == "" {
		return nil, nil
	}
	bad := fail(http.StatusBadRequest, "invalid_request", "invalid cursor")
	if len(raw) > maxCursorBytes {
		return nil, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, bad
	}
	at, id, ok := strings.Cut(string(b), "|")
	if !ok {
		return nil, bad
	}
	when, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, bad
	}
	if _, _, _, err := parseLearningID(id); err != nil {
		return nil, bad
	}
	return &learningCursor{At: when, ID: id}, nil
}

// afterCursor keeps the sorted items that come after the cursor.
func afterCursor(items []Learning, c learningCursor) []Learning {
	out := items[:0]
	for _, item := range items {
		if item.At.Before(c.At) || (item.At.Equal(c.At) && item.ID < c.ID) {
			out = append(out, item)
		}
	}
	return out
}

// ---------- HTTP ----------

func (m *module) handleRecommendLearning(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	publicID := r.PathValue("learningId")
	nodeID, commentID, comment, err := parseLearningID(publicID)
	if err != nil {
		writeErr(w, err)
		return
	}
	raw, err := readObject(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	in, err := parseRecommendation(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	rec, err := m.recommendLearning(r.Context(), p, publicID, nodeID, commentID, comment, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// recommendLearning stores the recommendation. Agents and people with
// knowledge.write may recommend; viewers may not. The learning must still be
// open and the target a live entry in the learning's project. Text that looks
// like a credential is refused and cannot be confirmed here.
func (m *module) recommendLearning(ctx context.Context, p tenant.Principal, publicID, nodeID, commentID string, comment bool, in recommendationInput) (Recommendation, error) {
	var out Recommendation
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
		var ranges []SensitiveRange
		ranges = append(ranges, sensitiveRanges("text", item.Text)...)
		ranges = append(ranges, sensitiveRanges("lesson", in.Lesson)...)
		ranges = append(ranges, sensitiveRanges("reason", in.Reason)...)
		ranges = append(ranges, sensitiveRanges("source_summary", in.SourceSummary)...)
		if len(ranges) > 0 {
			return sensitiveLearning(ranges)
		}
		title := ""
		if in.Decision == "accept" {
			if in.RuleLayerID != "" {
				if in.Lesson == "" {
					return fail(http.StatusBadRequest, "invalid_request", "lesson is required for a rule draft")
				}
				title, err = rules.LockLearningTarget(ctx, tx, p, in.RuleLayerID, in.KnowledgeID, item.projectID)
			} else {
				title, err = recommendTarget(ctx, tx, p.TenantID, in.KnowledgeID, item.projectID)
			}
			if err != nil {
				return asKnowledgeRule(err)
			}
		}
		// Lock order: tenant and tree (the fence), the learning's advisory
		// lock above, then the rows the
		// recommendation references (key-share, so foreign keys find them
		// held), then the recommendation row, and the event counter last.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM nodes WHERE tenant_id=$1 AND id = ANY($2::uuid[]) ORDER BY id FOR KEY SHARE`,
			p.TenantID, nonEmpty(item.projectID, in.KnowledgeID, in.RuleLayerID)); err != nil {
			return err
		}
		var byName string
		if err := tx.QueryRow(ctx, `SELECT name FROM principals WHERE tenant_id=$1 AND id=$2::uuid FOR KEY SHARE`, p.TenantID, p.ID).Scan(&byName); err != nil {
			return err
		}
		var before json.RawMessage
		err = tx.QueryRow(ctx, `SELECT json_build_object('decision', decision, 'knowledge_id', knowledge_id, 'lesson', lesson,
		    'reason', reason, 'source_summary', source_summary, 'changelog_line', changelog_line, 'rule_layer_id', rule_layer_id, 'text', learning_text, 'recommended_by', recommended_by, 'event_id', event_id)
		  FROM method_learning_recommendations WHERE tenant_id=$1 AND source_key=$2 FOR UPDATE`, p.TenantID, publicID).Scan(&before)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var date string
		if err := tx.QueryRow(ctx, `SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD')`).Scan(&date); err != nil {
			return err
		}
		line := ""
		if in.Decision == "accept" && in.RuleLayerID == "" && in.Lesson != "" {
			line = learningLine(date, in.Lesson, item.Key, item.Href)
		}
		change := events.Change{NodeID: &item.NodeID, Type: evLearningRecommended, After: recommendationAudit{
			SourceKey: publicID, ProjectID: item.projectID, NodeID: item.NodeID, CommentID: item.CommentID,
			Decision: in.Decision, KnowledgeID: in.KnowledgeID, Lesson: in.Lesson, Reason: in.Reason, Text: item.Text, SourceSummary: in.SourceSummary, Line: line, RuleLayerID: in.RuleLayerID,
		}}
		if before != nil {
			change.Before = before
		}
		ev, err := events.Append(ctx, tx, p, change)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO method_learning_recommendations
		    (tenant_id, source_key, project_id, decision, knowledge_id, lesson, reason, learning_text, recommended_by, event_id, recommended_at, source_summary, changelog_line, rule_layer_id)
		  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		  ON CONFLICT (tenant_id, source_key) DO UPDATE SET
		    project_id=EXCLUDED.project_id, decision=EXCLUDED.decision, knowledge_id=EXCLUDED.knowledge_id,
		    lesson=EXCLUDED.lesson, reason=EXCLUDED.reason, learning_text=EXCLUDED.learning_text,
		    recommended_by=EXCLUDED.recommended_by, recommended_at=EXCLUDED.recommended_at, event_id=EXCLUDED.event_id,
            source_summary=EXCLUDED.source_summary, changelog_line=EXCLUDED.changelog_line, rule_layer_id=EXCLUDED.rule_layer_id`,
			p.TenantID, publicID, item.projectID, in.Decision, nullable(in.KnowledgeID), nullable(in.Lesson), nullable(in.Reason),
			item.Text, p.ID, ev.ID, ev.At, nullable(in.SourceSummary), nullable(line), nullable(in.RuleLayerID)); err != nil {
			return err
		}
		out = Recommendation{
			Decision: in.Decision, KnowledgeID: in.KnowledgeID, KnowledgeTitle: title, Lesson: in.Lesson, Reason: in.Reason,
			By: &Person{ID: p.ID, Name: byName}, At: ev.At, EventID: ev.ID,
			SourceSummary: in.SourceSummary, Line: line, RuleLayerID: in.RuleLayerID, Prepared: in.Decision == "accept" && in.Lesson != "" && in.SourceSummary != "",
		}
		if out.RuleLayerID != "" {
			out.RuleText = out.Lesson
		}
		return nil
	})
	return out, err
}

// recommendTarget checks that id is a live knowledge entry, other than a
// Decision, in the learning's project and returns its title.
func recommendTarget(ctx context.Context, tx pgx.Tx, tenantID, id, projectID string) (string, error) {
	var kind, title string
	err := tx.QueryRow(ctx, `SELECT k.slug, n.title FROM nodes n
	  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	  WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL`, tenantID, id).Scan(&kind, &title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	if err != nil {
		return "", err
	}
	if _, ok := specFor(kind); !ok {
		return "", errNotFound
	}
	if kind == "decision" {
		return "", fail(http.StatusBadRequest, "invalid_request", "Decisions take no changelog lines; choose another entry")
	}
	project, err := projectOf(ctx, tx, tenantID, id)
	if err != nil {
		return "", err
	}
	if project != projectID {
		return "", errNotFound
	}
	return title, nil
}

// attachRecommendations adds the current recommendation to each listed item.
func attachRecommendations(ctx context.Context, tx pgx.Tx, tenantID, projectID string, items []Learning) error {
	if len(items) == 0 {
		return nil
	}
	keys := make([]string, len(items))
	index := make(map[string]int, len(items))
	for i, item := range items {
		keys[i] = item.ID
		index[item.ID] = i
	}
	rows, err := tx.Query(ctx, `SELECT r.source_key, r.decision, coalesce(r.knowledge_id::text, ''), coalesce(r.lesson, ''),
	    coalesce(r.reason, ''), r.learning_text, r.recommended_at, r.event_id, who.id::text, who.name,
	    coalesce(r.source_summary,''), coalesce(r.changelog_line,''), coalesce(r.rule_layer_id::text,''), coalesce(n.title, ''), (n.id IS NOT NULL AND n.deleted_at IS NULL AND proj.id IS NOT DISTINCT FROM $3::uuid)
	  FROM method_learning_recommendations r
	  JOIN principals who ON who.tenant_id=r.tenant_id AND who.id=r.recommended_by
	  LEFT JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.knowledge_id
	  `+nearestProject+`
	  WHERE r.tenant_id=$1 AND r.source_key = ANY($2::text[])`, tenantID, keys, projectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, text, byID, byName string
		var live bool
		var rec Recommendation
		if err := rows.Scan(&key, &rec.Decision, &rec.KnowledgeID, &rec.Lesson, &rec.Reason, &text, &rec.At, &rec.EventID,
			&byID, &byName, &rec.SourceSummary, &rec.Line, &rec.RuleLayerID, &rec.KnowledgeTitle, &live); err != nil {
			return err
		}
		at, ok := index[key]
		if !ok {
			continue
		}
		rec.By = &Person{ID: byID, Name: byName}
		rec.Stale = text != items[at].Text
		if rec.Decision == "accept" && !live {
			rec.TargetMissing = true
			rec.KnowledgeTitle = ""
		}
		items[at].Recommendation = &rec
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for i := range items {
		rec := items[i].Recommendation
		if rec == nil {
			continue
		}
		if rec.RuleLayerID != "" {
			rec.RuleText = rec.Lesson
			rec.TargetMissing = true
			if p, ok := tenant.PrincipalFrom(ctx); ok {
				title, err := rules.LearningTarget(ctx, tx, p, rec.RuleLayerID, rec.KnowledgeID, projectID)
				if err == nil {
					rec.KnowledgeTitle, rec.TargetMissing = title, false
				} else {
					var re *rules.Error
					if !errors.As(err, &re) && !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, authz.ErrForbidden) {
						return err
					}
				}
			}
		}
		rec.Prepared = rec.Decision == "accept" && rec.Lesson != "" && rec.SourceSummary != "" && !rec.Stale && !rec.TargetMissing && (rec.Line != "" || rec.RuleLayerID != "")
	}
	return nil
}

// Filter before source scan limits so a large unprepared window cannot hide
// older prepared lessons. Live source and target checks follow the scan.
type preparedListKey struct{}

func preparedClause(ctx context.Context, sourceKey string) string {
	if ctx.Value(preparedListKey{}) != true {
		return ""
	}
	return ` AND EXISTS (SELECT 1 FROM method_learning_recommendations ready
      WHERE ready.tenant_id=n.tenant_id AND ready.source_key=` + sourceKey + `
      AND ready.decision='accept' AND btrim(coalesce(ready.lesson,''))<>''
      AND btrim(coalesce(ready.source_summary,''))<>''
      AND (btrim(coalesce(ready.changelog_line,''))<>'' OR ready.rule_layer_id IS NOT NULL)) `
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
