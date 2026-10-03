// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
)

// learningDraftAudit is knowledge.learning_drafted. Undo reads rule back out
// of it; the event is not a node snapshot. The rule layer and set are not in
// it: every UUID in an event that names a node becomes one of the event's
// node references, and a person who sees only this project cannot see rules
// layer or set nodes, so the event (and its undo) would be hidden from the
// very person who drafted it. The decision row keeps them for undo.
type learningDraftAudit struct {
	SourceKey string     `json:"source_key"`
	Source    string     `json:"source"`
	NodeID    string     `json:"node_id"`
	CommentID string     `json:"comment_id,omitempty"`
	ProjectID string     `json:"project_id"`
	Text      string     `json:"text"`
	Rule      rules.Rule `json:"rule"`
	// A person confirmed that a suspected credential is not one.
	ConfirmedNotSensitive bool `json:"confirmed_not_sensitive,omitempty"`
}

func (m *module) handleDraftLearning(w http.ResponseWriter, r *http.Request) {
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
	layerID, setID, confirm, err := parseDraft(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	decision, err := m.draftLearning(r.Context(), p, r.PathValue("learningId"), nodeID, commentID, comment, layerID, setID, confirm)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decision)
}

func parseDraft(raw map[string]json.RawMessage) (string, string, bool, error) {
	if len(raw) == 0 {
		return "", "", false, fail(http.StatusBadRequest, "invalid_request", "layer_id and set_id are required")
	}
	for key := range raw {
		if key != "layer_id" && key != "set_id" && key != "confirm_not_sensitive" {
			return "", "", false, fail(http.StatusBadRequest, "invalid_request", "unknown field "+key)
		}
	}
	layerID, _, err := stringField(raw, "layer_id")
	if err != nil {
		return "", "", false, err
	}
	setID, _, err := stringField(raw, "set_id")
	if err != nil {
		return "", "", false, err
	}
	layerID, setID = strings.TrimSpace(layerID), strings.TrimSpace(setID)
	if !canonicalUUID(layerID) || !canonicalUUID(setID) {
		return "", "", false, fail(http.StatusBadRequest, "invalid_request", "layer_id and set_id are required")
	}
	confirm, err := confirmField(raw)
	if err != nil {
		return "", "", false, err
	}
	return layerID, setID, confirm, nil
}

func canonicalUUID(s string) bool {
	return validUUID(s) && s == strings.ToLower(s)
}

func (m *module) draftLearning(ctx context.Context, p tenant.Principal, publicID, nodeID, commentID string, comment bool, layerID, setID string, confirm bool) (LearningDecision, error) {
	var out LearningDecision
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if !canWrite(ctx, tx, p, "knowledge.write") {
			return fail(http.StatusForbidden, "forbidden", "you can read knowledge but not change it")
		}
		// Enter tenant/tree before learning/resource locks.
		if err := rules.PrepareWrite(ctx, tx, p); err != nil {
			return asKnowledgeRule(err)
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
		confirmed, err := sensitiveCheck(append(sensitiveRanges("text", item.Text), sensitiveRanges("title", item.Title)...), confirm)
		if err != nil {
			return err
		}
		rule, err := learningRule(item, publicID, nodeID, commentID, comment)
		if err != nil {
			return err
		}
		audit := learningDraftAudit{
			SourceKey: publicID, Source: item.Source, NodeID: item.NodeID, CommentID: item.CommentID,
			ProjectID: item.projectID, Text: item.Text, Rule: rule, ConfirmedNotSensitive: confirmed,
		}
		ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &item.NodeID, Type: evLearningDrafted, After: audit})
		if err != nil {
			return err
		}
		if err = insertDecision(ctx, tx, p, item.projectID, publicID, "drafted", nil, nil, nil, ev.ID, &setID, &layerID, &rule.Identity); err != nil {
			return err
		}
		if _, err = rules.AppendLearningRule(ctx, tx, p, layerID, setID, item.projectID, rule); err != nil {
			return asKnowledgeRule(err)
		}
		out = LearningDecision{
			ID: publicID, Decision: "drafted", EventID: ev.ID,
			RuleSetID: setID, RuleLayerID: layerID, RuleIdentity: rule.Identity,
		}
		return nil
	})
	return out, err
}

func learningRule(item Learning, publicID, nodeID, commentID string, comment bool) (rules.Rule, error) {
	text := clipBytes(item.Text, 512)
	identity := "learn.n." + nodeID
	if comment {
		identity = "learn.c." + nodeID + "." + commentID
	}
	why := firstLine("From "+item.Key, "Method learning "+item.Key, "From "+publicID)
	reference := item.Key
	if comment {
		reference = item.Key + "#" + commentID
	}
	if !fitsLine(reference, 512) {
		reference = publicID
	}
	if !fitsLine(text, 512) || !fitsLine(why, 1024) || !fitsLine(reference, 512) || !fitsLine(publicID, 128) || !fitsLine(nodeID, 96) {
		return rules.Rule{}, fail(http.StatusBadRequest, "invalid_rule", "This learning cannot become a rule.")
	}
	return rules.Rule{
		Identity: identity,
		Text:     text,
		Why:      why,
		Details:  ruleDetails(item, publicID),
		Strength: "normal",
		Enabled:  true,
		Source: rules.Source{
			Reference:  reference,
			Revision:   publicID,
			Identity:   nodeID,
			EditedHere: false,
		},
	}, nil
}

func firstLine(candidates ...string) string {
	for _, candidate := range candidates {
		if fitsLine(candidate, 1024) {
			return candidate
		}
	}
	return ""
}

func ruleDetails(item Learning, publicID string) string {
	var b strings.Builder
	b.WriteString("Learning " + publicID + "\n")
	b.WriteString("Node " + item.NodeID + "\n")
	if item.CommentID != "" {
		b.WriteString("Comment " + item.CommentID + "\n")
	}
	b.WriteString("Key " + item.Key + "\n")
	b.WriteString("Title " + item.Title + "\n")
	b.WriteString("Link " + item.Href + "\n")
	return clipBytes(b.String(), 16384)
}

func clipBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for s != "" && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.TrimSpace(s)
}

func fitsLine(s string, max int) bool {
	if !utf8.ValidString(s) || len(s) > max || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func asKnowledgeRule(err error) error {
	if err == nil {
		return nil
	}
	var re *rules.Error
	if errors.As(err, &re) {
		msg := re.Message
		if re.Code == "revision_conflict" {
			msg = "That rule set changed. Open it and try again."
		}
		return fail(re.Status, re.Code, msg)
	}
	if errors.Is(err, authz.ErrForbidden) {
		return fail(http.StatusForbidden, "rule_forbidden", "You cannot draft rules in that set.")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(http.StatusNotFound, "rule_unavailable", "That rule set is not available.")
	}
	return err
}

func undoLearningDrafted(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if p.Kind != tenant.Person || !canWrite(ctx, tx, p, "knowledge.write") {
		return events.Change{}, events.ErrForbidden
	}
	var audit learningDraftAudit
	if json.Unmarshal(e.After, &audit) != nil || e.NodeID == nil || audit.NodeID != *e.NodeID || audit.SourceKey == "" || audit.Rule.Identity == "" {
		return events.Change{}, events.ErrConflict
	}
	if err := rules.PrepareWrite(ctx, tx, p); err != nil {
		return events.Change{}, undoRuleErr(err)
	}
	if err := lockLearning(ctx, tx, p.TenantID, audit.SourceKey); err != nil {
		return events.Change{}, err
	}
	var layerID, setID, projectID string
	err := tx.QueryRow(ctx, `DELETE FROM method_learning_decisions
		WHERE tenant_id=$1 AND event_id=$2 AND decision='drafted' AND source_key=$3
		RETURNING rule_layer_id::text, rule_set_id::text, project_id::text`, p.TenantID, e.ID, audit.SourceKey).Scan(&layerID, &setID, &projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return events.Change{}, events.ErrConflict
	}
	if err != nil {
		return events.Change{}, err
	}
	if err = rules.RemoveLearningRule(ctx, tx, p, layerID, setID, projectID, audit.Rule); err != nil {
		return events.Change{}, undoRuleErr(err)
	}
	return events.Change{NodeID: e.NodeID, Type: evLearningDrafted, Before: json.RawMessage(e.After), After: map[string]any{"decision": "reopened"}}, nil
}

func undoRuleErr(err error) error {
	var re *rules.Error
	if errors.As(err, &re) {
		if re.Status == http.StatusForbidden {
			return events.ErrForbidden
		}
		if re.Status == http.StatusNotFound || re.Status == http.StatusConflict {
			return events.ErrConflict
		}
	}
	if errors.Is(err, authz.ErrForbidden) {
		return events.ErrForbidden
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return events.ErrConflict
	}
	return err
}
