// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// One adapter consumes the existing AEON-444 inbox; the desk has no git writer.
type DoctrineAdapter interface {
	PrepareDeskDraft(context.Context, tenant.Principal, doctrine.InboxInput) (*doctrine.PreparedInbox, error)
	CheckDeskTargetTx(context.Context, pgx.Tx, tenant.Principal, doctrine.InboxInput) error
	RecordDeskDraftTx(context.Context, pgx.Tx, tenant.Principal, doctrine.InboxInput, string, string, string) (string, []events.Change, error)
	RetireDeskDraftTx(context.Context, pgx.Tx, tenant.Principal, string, string, string) ([]events.Change, error)
}

type outcomeInput struct {
	TicketID       string          `json:"ticket_id,omitempty"`
	TicketRevision time.Time       `json:"ticket_revision,omitempty"`
	Doctrine       *DoctrineTarget `json:"doctrine,omitempty"`
}

// EffectData keeps the exact appended text and service-owned identities. It is
// retained on superseded revisions, so a correction can remove only its effect.
type EffectData struct {
	Retryable      *bool          `json:"retryable,omitempty"`
	ReviewRequired []EffectReview `json:"review_required,omitempty"`
	TicketID       string         `json:"ticket_id,omitempty"`
	Criterion      string         `json:"criterion,omitempty"`
	KnowledgeID    string         `json:"knowledge_id,omitempty"`
	DoctrineID     string         `json:"doctrine_id,omitempty"`
	Supersedes     string         `json:"supersedes,omitempty"`
	SupersededBy   string         `json:"superseded_by,omitempty"`
}

type EffectReview struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Why  string `json:"why"`
}

func doctrineInput(target *DoctrineTarget) doctrine.InboxInput {
	if target == nil {
		return doctrine.InboxInput{}
	}
	in := doctrine.InboxInput{SourceID: target.SourceID, Path: target.Path, RuleKey: target.RuleKey, RuleSHA: target.RuleSHA}
	if target.TLDREN != "" || target.TLDRDE != "" {
		in.TLDR = &doctrine.InboxTLDR{EN: target.TLDREN, DE: target.TLDRDE}
	}
	return in
}

func (m *Module) availability(ctx context.Context, tx pgx.Tx, p tenant.Principal, q Question, target *DoctrineTarget, check authz.ProjectCheck) ([]OutcomeAvailability, error) {
	out := []OutcomeAvailability{{Outcome: "once"}, {Outcome: "always"}, {Outcome: "requirement"}, {Outcome: "doctrine"}}
	for i := range out {
		s := &out[i]
		if p.Kind != tenant.Person || p.KeyCreatorID != "" {
			s.Why = "A signed-in person chooses this outcome."
			continue
		}
		if !check("questions.decide", q.ProjectID) {
			s.Why = "Question decision permission is required."
			continue
		}
		permission := ""
		switch s.Outcome {
		case "always":
			permission = "knowledge.write"
		case "requirement":
			permission = "nodes.write"
		}
		if permission != "" && !check(permission, q.ProjectID) {
			s.Why = "This outcome requires " + permission + " permission."
			continue
		}
		if s.Outcome == "requirement" {
			if q.Input.TicketID == "" {
				s.Why = "Requirement needs a linked editable ticket."
				continue
			}
			var editable, textCriteria bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND n.project_id=$2 AND n.id=$3 AND n.deleted_at IS NULL
 AND (k.slug IN ('ticket','task','epic') OR k.field_schema->>'issue_family'='true')),
 COALESCE((SELECT NOT (fields ? 'acceptance_criteria') OR fields->'acceptance_criteria'='null'::jsonb OR jsonb_typeof(fields->'acceptance_criteria')='string' FROM nodes WHERE tenant_id=$1 AND id=$3),false)`, p.TenantID, q.ProjectID, q.Input.TicketID).Scan(&editable, &textCriteria); err != nil {
				return nil, err
			}
			if !editable {
				s.Why = "The linked ticket is no longer editable."
				continue
			}
			if !textCriteria {
				s.Why = "Requirement needs text acceptance criteria; review the ticket's list or structured criteria first."
				continue
			}
		}
		if s.Outcome == "doctrine" {
			if target == nil {
				s.Why = "Doctrine needs an authorized source, rule and exact base mapping."
				continue
			}
			if m.doctrine == nil {
				s.Why = "The doctrine inbox adapter is unavailable."
				continue
			}
			if !check("rules.write", "") {
				s.Why = "Doctrine requires workspace rules.write permission."
				continue
			}
			s.MappingPresent = true

		}
		s.Available = true
	}
	return out, nil
}

func (m *Module) applyOutcome(ctx context.Context, tx pgx.Tx, tid string, d delivery) error {
	p := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	var a Answer
	var in outcomeInput
	var questionInput Input
	if err := tx.QueryRow(ctx, `SELECT a.node_id::text,a.answer,a.reason,a.outcome,a.decided_by::text,a.outcome_data,q.input
 FROM desk_answers a JOIN desk_questions q ON q.tenant_id=a.tenant_id AND q.node_id=a.question_id
 JOIN principals p ON p.tenant_id=a.tenant_id AND p.id=a.decided_by AND p.kind='person'
 WHERE a.tenant_id=$1 AND a.question_id=$2 AND a.revision=$3`, tid, d.question, d.revision).Scan(&a.ID, &a.Answer, &a.Reason, &a.Outcome, &p.ID, &in, &questionInput); err != nil {
		return err
	}
	for _, permission := range []string{"questions.read", "questions.decide"} {
		if err := permit(ctx, tx, p, d.project, permission); err != nil {
			return fail(403, "answerer_access_lost", "The answerer's access changed; the outcome has not been applied.")
		}
	}
	if err := checkProject(ctx, tx, p, d.project); err != nil {
		return err
	}
	if err := checkNode(ctx, tx, tid, d.project, d.question, false); err != nil {
		return err
	}
	q := Question{ID: d.question, ProjectID: d.project, Input: questionInput}
	check, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return err
	}
	stamps, err := m.availability(ctx, tx, p, q, in.Doctrine, check)
	if err != nil {
		return err
	}
	for _, stamp := range stamps {
		if stamp.Outcome == a.Outcome && !stamp.Available {
			return fail(422, "outcome_unavailable", stamp.Why)
		}
	}

	var previousID string
	var previous EffectData
	err = tx.QueryRow(ctx, `SELECT id::text,effect_data FROM desk_pending WHERE tenant_id=$1 AND question_id=$2
 AND revision<$3 AND kind='outcome' AND state='delivered' AND effect_ref<>'' ORDER BY revision DESC LIMIT 1 FOR NO KEY UPDATE`, tid, d.question, d.revision).Scan(&previousID, &previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	changes := []events.Change{}
	data := EffectData{Supersedes: previousID, ReviewRequired: slices.Clone(previous.ReviewRequired)}
	previousCriterion := previous
	if previousCriterion.TicketID != "" {
		editable := check("nodes.write", d.project)
		if editable {
			if err := checkNode(ctx, tx, tid, d.project, previousCriterion.TicketID, true); err != nil {
				var ae *apiError
				if !errors.As(err, &ae) || ae.status != 404 {
					return err
				}
				editable = false
			}
		}
		if !editable {
			data.requireReview(EffectReview{Kind: "criterion", Ref: previousCriterion.TicketID, Why: "The earlier ticket is missing or no longer editable; its tracked criterion was preserved for person review."})
			previousCriterion.TicketID, previousCriterion.Criterion = "", ""
		}
	}
	if previousCriterion.TicketID != "" || a.Outcome == "requirement" {
		if err := permit(ctx, tx, p, d.project, "nodes.write"); err != nil {
			return fail(403, "ticket_access_lost", "Updating the criterion requires ticket write permission.")
		}
		ticket := previousCriterion.TicketID
		if a.Outcome == "requirement" {
			ticket = in.TicketID
		}
		if previousCriterion.TicketID != "" && previousCriterion.TicketID != ticket {
			return fail(409, "ticket_changed", "The linked criterion belongs to another ticket; review the correction.")
		}
		if err := checkNode(ctx, tx, tid, d.project, ticket, true); err != nil {
			return err
		}
		var criteriaJSON json.RawMessage
		var revision time.Time
		var beforeNode, afterNode json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT fields->'acceptance_criteria',updated_at,to_jsonb(nodes) FROM nodes WHERE tenant_id=$1 AND id=$2 FOR NO KEY UPDATE`, tid, ticket).Scan(&criteriaJSON, &revision, &beforeNode); err != nil {
			return err
		}
		if a.Outcome == "requirement" && !revision.Equal(in.TicketRevision) {
			return fail(409, "ticket_revision_conflict", "The ticket changed during grace; review it and decide again. No criterion was changed.")
		}
		criteria := ""
		textCriteria := len(criteriaJSON) == 0 || string(criteriaJSON) == "null" || json.Unmarshal(criteriaJSON, &criteria) == nil
		if !textCriteria && a.Outcome == "requirement" {
			return fail(409, "criteria_format_conflict", "The ticket's acceptance criteria are not text; review them before appending.")
		}
		if len(criteria) > 64<<10 {
			return fail(422, "criteria_too_large", "The ticket's criteria exceed the bounded append limit.")
		}
		changed := false
		if previousCriterion.Criterion != "" {
			if !textCriteria || strings.Count(criteria, previousCriterion.Criterion) != 1 {
				data.requireReview(EffectReview{Kind: "criterion", Ref: ticket, Why: "The earlier tracked criterion was edited or removed; it was preserved for person review."})
			} else {
				criteria = strings.Replace(criteria, previousCriterion.Criterion, "", 1)
				changed = true
			}
		}
		if a.Outcome == "requirement" {
			// Escape line breaks so the answer is exactly one tracked criterion.
			text := strings.Join(strings.Fields(a.Answer), " ")
			data.TicketID = ticket
			data.Criterion = "\n- [ ] " + text + " <!-- decision-desk:" + a.ID + " -->"
			criteria += data.Criterion
			changed = true
		}
		if len(criteria) > 64<<10 {
			return fail(422, "criteria_too_large", "The appended criteria would exceed the bounded limit.")
		}
		if changed {
			raw, err := json.Marshal(criteria)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE nodes SET fields=jsonb_set(fields,'{acceptance_criteria}',$3::jsonb),updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE tenant_id=$1 AND id=$2`, tid, ticket, raw); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT to_jsonb(nodes) FROM nodes WHERE tenant_id=$1 AND id=$2`, tid, ticket).Scan(&afterNode); err != nil {
				return err
			}
			changes = append(changes, events.Change{NodeID: &ticket, Type: "node.updated", Before: beforeNode, After: afterNode, Metadata: json.RawMessage(`{"reason":"Decision Desk criterion"}`)})
		}

	}
	reviewDoctrine := false
	for _, review := range data.ReviewRequired {
		if review.Kind == "doctrine" {
			reviewDoctrine = true
		}
	}
	if previous.DoctrineID != "" {
		if m.doctrine == nil {
			return fail(503, "doctrine_unavailable", "The earlier doctrine draft cannot be corrected until the adapter is available.")
		}
		more, err := m.doctrine.RetireDeskDraftTx(ctx, tx, p, previous.DoctrineID, d.question, a.ID)
		if err != nil {
			code, why := doctrine.DeskFailure(err)
			if code != "doctrine_review_required" {
				return doctrineOutcomeError(err)
			}
			data.requireReview(EffectReview{Kind: "doctrine", Ref: previous.DoctrineID, Why: why})
			reviewDoctrine = true
		}
		changes = append(changes, more...)
	}
	ref := a.ID
	state := "decided"
	switch a.Outcome {
	case "always":
		data.KnowledgeID = a.ID
		fields := map[string]any{"slug": "decision-" + a.ID, "metadata": map[string]any{"question_id": d.question, "answer_id": a.ID, "revision": d.revision, "decision_state": "active", "supersedes": previous.KnowledgeID, "decided_by": p.ID, "reason": a.Reason}}
		raw, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		title := questionInput.Question
		if len(title) > 500 {
			title = title[:500]
			for !utf8.ValidString(title) {
				title = title[:len(title)-1]
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET title=$3,body=$4,fields=$5,updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE tenant_id=$1 AND id=$2`, tid, a.ID, title, a.Answer, raw); err != nil {
			return err
		}
		state = "active"
		var afterNode json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(nodes) FROM nodes WHERE tenant_id=$1 AND id=$2`, tid, a.ID).Scan(&afterNode); err != nil {
			return err
		}
		changes = append(changes, events.Change{NodeID: &a.ID, Type: "knowledge.created", After: afterNode})
	case "doctrine":
		if reviewDoctrine {
			break
		}
		if d.prepareErr != nil {
			return d.prepareErr
		}
		if d.prepared == nil {
			return fail(503, "doctrine_unavailable", "The draft preparation is unavailable; it will be retried.")
		}
		inbox := d.doctrineInput
		id, more, err := m.doctrine.RecordDeskDraftTx(doctrine.WithPreparedInbox(ctx, d.prepared), tx, p, inbox, d.question, a.ID, "")
		if err != nil {
			return doctrineOutcomeError(err)
		}
		data.DoctrineID, ref = id, id
		changes = append(changes, more...)
	case "requirement":
		ref = fmt.Sprintf("criterion/%s/%s", data.TicketID, a.ID)
	}
	if previousID != "" {
		previous.SupersededBy = d.id
		raw, err := json.Marshal(previous)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE desk_pending SET effect_data=$3 WHERE tenant_id=$1 AND id=$2`, tid, previousID, raw); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE desk_decisions SET state=$4,effect_ref=$5 WHERE tenant_id=$1 AND question_id=$2 AND revision=$3`, tid, d.question, d.revision, state, ref); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE desk_pending SET state='delivered',effect_ref=$3,effect_data=$4,error_code='',error_message='',retry_at=NULL WHERE tenant_id=$1 AND id=$2`, tid, d.id, ref, raw); err != nil {
		return err
	}
	changes = append(changes, events.Change{NodeID: &d.question, Type: "question.outcome_applied", After: map[string]any{"answer_id": a.ID, "revision": d.revision, "outcome": a.Outcome, "effect_id": d.id, "effect_ref": ref, "effect_data": data}})
	// No row locks or mutations follow the event counter.
	for _, change := range changes {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return err
		}
	}
	return nil
}

func doctrineOutcomeError(err error) error {
	code, why := doctrine.DeskFailure(err)
	status := 409
	switch code {
	case "doctrine_forbidden":
		status = 403
	case "doctrine_failed", "guard_unavailable", "busy":
		status = 503
	}
	return fail(status, code, why)
}

func (data *EffectData) requireReview(review EffectReview) {
	for i, existing := range data.ReviewRequired {
		if existing.Kind == review.Kind && existing.Ref == review.Ref {
			data.ReviewRequired[i] = review
			return
		}
	}
	data.ReviewRequired = append(data.ReviewRequired, review)
}
