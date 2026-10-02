// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	TicketID     string `json:"ticket_id,omitempty"`
	Criterion    string `json:"criterion,omitempty"`
	KnowledgeID  string `json:"knowledge_id,omitempty"`
	DoctrineID   string `json:"doctrine_id,omitempty"`
	Supersedes   string `json:"supersedes,omitempty"`
	SupersededBy string `json:"superseded_by,omitempty"`
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

func (m *Module) availability(ctx context.Context, tx pgx.Tx, p tenant.Principal, q Question, target *DoctrineTarget) ([]OutcomeAvailability, error) {
	out := []OutcomeAvailability{{Outcome: "once"}, {Outcome: "always"}, {Outcome: "requirement"}, {Outcome: "doctrine"}}
	for i := range out {
		s := &out[i]
		if p.Kind != tenant.Person || p.KeyCreatorID != "" {
			s.Why = "A signed-in person chooses this outcome."
			continue
		}
		if err := authz.RequireTx(ctx, tx, p, "questions.decide", authz.Scope{ProjectID: q.ProjectID}); err != nil {
			if !errors.Is(err, authz.ErrForbidden) {
				return nil, err
			}
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
		if permission != "" {
			if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: q.ProjectID}); err != nil {
				if !errors.Is(err, authz.ErrForbidden) {
					return nil, err
				}
				s.Why = "This outcome requires " + permission + " permission."
				continue
			}
		}
		if s.Outcome == "requirement" {
			if q.Input.TicketID == "" {
				s.Why = "Requirement needs a linked editable ticket."
				continue
			}
			if err := checkNode(ctx, tx, p.TenantID, q.ProjectID, q.Input.TicketID, true); err != nil {
				s.Why = "The linked ticket is no longer editable."
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
			if err := m.doctrine.CheckDeskTargetTx(ctx, tx, p, doctrineInput(target)); err != nil {
				_, s.Why = doctrine.DeskFailure(err)
				continue
			}
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
	stamps, err := m.availability(ctx, tx, p, q, in.Doctrine)
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
	data := EffectData{Supersedes: previousID}
	if previous.KnowledgeID != "" {
		if err := permit(ctx, tx, p, d.project, "knowledge.write"); err != nil {
			return fail(403, "knowledge_access_lost", "Replacing the earlier Decision requires knowledge.write permission.")
		}
		tag, err := tx.Exec(ctx, `UPDATE nodes SET state='cancelled',fields=jsonb_set(jsonb_set(fields,'{metadata,decision_state}','"superseded"'),'{metadata,superseded_by}',to_jsonb($3::text)),
 updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE tenant_id=$1 AND id=$2 AND fields->'metadata'->>'question_id'=$4 AND deleted_at IS NULL`, tid, previous.KnowledgeID, a.ID, d.question)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(409, "effect_ownership_conflict", "The earlier Decision changed; review its provenance before correcting it.")
		}
		changes = append(changes, events.Change{NodeID: &previous.KnowledgeID, Type: "knowledge.superseded", After: map[string]any{"superseded_by": a.ID, "question_id": d.question}})
	}
	if previous.TicketID != "" || a.Outcome == "requirement" {
		if err := permit(ctx, tx, p, d.project, "nodes.write"); err != nil {
			return fail(403, "ticket_access_lost", "Updating the criterion requires ticket write permission.")
		}
		ticket := previous.TicketID
		if a.Outcome == "requirement" {
			ticket = in.TicketID
		}
		if previous.TicketID != "" && previous.TicketID != ticket {
			return fail(409, "ticket_changed", "The linked criterion belongs to another ticket; review the correction.")
		}
		if err := checkNode(ctx, tx, tid, d.project, ticket, true); err != nil {
			return err
		}
		var fields map[string]any
		var revision time.Time
		var beforeNode, afterNode json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT fields,updated_at,to_jsonb(nodes) FROM nodes WHERE tenant_id=$1 AND id=$2 FOR NO KEY UPDATE`, tid, ticket).Scan(&fields, &revision, &beforeNode); err != nil {
			return err
		}
		if a.Outcome == "requirement" && !revision.Equal(in.TicketRevision) {
			return fail(409, "ticket_revision_conflict", "The ticket changed during grace; review it and decide again. No criterion was changed.")
		}
		criteria, ok := fields["acceptance_criteria"].(string)
		if fields["acceptance_criteria"] != nil && !ok {
			return fail(409, "criteria_format_conflict", "The ticket's acceptance criteria are not text; review them before appending.")
		}
		if len(criteria) > 64<<10 {
			return fail(422, "criteria_too_large", "The ticket's criteria exceed the bounded append limit.")
		}
		if previous.Criterion != "" {
			if strings.Count(criteria, previous.Criterion) != 1 {
				return fail(409, "criterion_changed", "The earlier tracked criterion was edited or removed; review the correction. No other criterion was changed.")
			}
			criteria = strings.Replace(criteria, previous.Criterion, "", 1)
		}
		if a.Outcome == "requirement" {
			// Escape line breaks so the answer is exactly one tracked criterion.
			text := strings.Join(strings.Fields(a.Answer), " ")
			data.TicketID = ticket
			data.Criterion = "\n- [ ] " + text + " <!-- decision-desk:" + a.ID + " -->"
			criteria += data.Criterion
		}
		if len(criteria) > 64<<10 {
			return fail(422, "criteria_too_large", "The appended criteria would exceed the bounded limit.")
		}
		if fields == nil {
			fields = map[string]any{}
		}
		fields["acceptance_criteria"] = criteria
		raw, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET fields=$3,updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE tenant_id=$1 AND id=$2`, tid, ticket, raw); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(nodes) FROM nodes WHERE tenant_id=$1 AND id=$2`, tid, ticket).Scan(&afterNode); err != nil {
			return err
		}
		changes = append(changes, events.Change{NodeID: &ticket, Type: "node.updated", Before: beforeNode, After: afterNode, Metadata: json.RawMessage(`{"reason":"Decision Desk criterion"}`)})
	}
	if previous.DoctrineID != "" && a.Outcome != "doctrine" {
		if m.doctrine == nil {
			return fail(503, "doctrine_unavailable", "The earlier doctrine draft cannot be corrected until the adapter is available.")
		}
		more, err := m.doctrine.RetireDeskDraftTx(ctx, tx, p, previous.DoctrineID, d.question, a.ID)
		if err != nil {
			code, why := doctrine.DeskFailure(err)
			return fail(409, code, why)
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
		changes = append(changes, events.Change{NodeID: &a.ID, Type: "knowledge.created", After: map[string]any{"id": a.ID, "title": title, "body": a.Answer, "fields": fields, "state": "backlog"}})
	case "doctrine":
		inbox := doctrineInput(in.Doctrine)
		inbox.RequestID, inbox.Source, inbox.Why = a.ID, a.Answer, a.Reason
		if inbox.Why == "" {
			inbox.Why = "Human answer recorded on Decision Desk question " + d.question
		}
		if questionInput.TicketID != "" {
			if err := tx.QueryRow(ctx, `SELECT key FROM nodes WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`, tid, questionInput.TicketID).Scan(&inbox.Ticket); err != nil {
				return err
			}
		}
		id, more, err := m.doctrine.RecordDeskDraftTx(ctx, tx, p, inbox, d.question, a.ID, previous.DoctrineID)
		if err != nil {
			code, why := doctrine.DeskFailure(err)
			return fail(409, code, why)
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
