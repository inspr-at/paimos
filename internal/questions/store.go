// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func digest(v any) (string, []byte, error) {
	b, e := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), b, e
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func permit(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, permission string) error {
	if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return missing()
		}
		return err
	}
	return nil
}
func writeCapability(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT set_config('aeon.desk_write','on',true)`)
	return err
}

// Node creation already serializes the tenant tree. Take that lock first on
// all mutations, then session/question locks, avoiding inverse lock ordering.
func treeLock(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, tenantID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID)
	return err
}
func checkNode(ctx context.Context, tx pgx.Tx, tenantID, project, id string, ticket bool) error {
	var found bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND n.project_id=$2 AND n.id=$3 AND n.deleted_at IS NULL
 AND (NOT $4::boolean OR k.slug IN ('ticket','task','epic') OR k.field_schema->>'issue_family'='true'))`, tenantID, project, id, ticket).Scan(&found)
	if err != nil {
		return err
	}
	if !found {
		return missing()
	}
	return nil
}
func checkProject(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) error {
	var found bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND n.id=$2 AND n.project_id=n.id AND k.slug='project' AND n.deleted_at IS NULL)`, p.TenantID, project).Scan(&found)
	if err != nil {
		return err
	}
	if !found {
		return missing()
	}
	return nil
}
func createNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, parent, kind string) (string, error) {
	prefix, label := "QST", "Question"
	if kind == "decision" {
		prefix, label = "DCS", "Decision"
	}
	var kindID, id string
	if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,field_schema)
 VALUES($1,$2,$3,$4,'question','{"issue_family":false}') ON CONFLICT(tenant_id,slug) DO NOTHING`, p.TenantID, kind, label, prefix); err != nil {
		return "", err
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM node_kinds WHERE tenant_id=$1 AND slug=$2`, p.TenantID, kind).Scan(&kindID); err != nil {
		return "", err
	}
	// Generic node/search/events readers see only neutral identity stubs. The
	// guarded question projection owns private text and authoritative state.
	err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
 VALUES($1,$2,aeon_next_node_key($1,$3),$4,$5) RETURNING id::text`, p.TenantID, kindID, prefix, label, parent).Scan(&id)
	return id, err
}
func event(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, kind string, revision int64) error {
	after, _ := json.Marshal(map[string]any{"question_id": id, "revision": revision})
	_, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after) VALUES($1,$2,$3,$4,$5::jsonb)`, p.TenantID, p.ID, id, kind, after)
	return err
}
func suggestion(in Input) (string, string) {
	if in.SuggestedOutcome != "" {
		return in.SuggestedOutcome, "agent_suggestion"
	}
	if in.TicketID != "" {
		return "once", "ticket_default"
	}
	return "always", "project_default"
}
func (m *Module) ask(ctx context.Context, p tenant.Principal, project string, in Input) (Question, bool, error) {
	var q Question
	replay := false
	hash, body, err := digest(in)
	if err != nil {
		return q, false, err
	}
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := treeLock(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if err := permit(ctx, tx, p, project, "questions.ask"); err != nil {
			return err
		}
		if err := permit(ctx, tx, p, project, "questions.read"); err != nil {
			return err
		}
		if err := checkProject(ctx, tx, p, project); err != nil {
			return err
		}
		var id, previous string
		err := tx.QueryRow(ctx, `SELECT question_id::text,request_digest FROM desk_askers WHERE tenant_id=$1 AND project_id=$2 AND principal_id=$3 AND request_id=$4`, p.TenantID, project, p.ID, in.RequestID).Scan(&id, &previous)
		if err == nil {
			if hash != previous {
				return fail(409, "replay_conflict", "request_id already identifies different input")
			}
			replay = true
			q, err = m.read(ctx, tx, p, id)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.SourceHandoverID != "" {
			return fail(422, "source_unavailable", "handover questions require the verified handover adapter")
		}
		if in.TicketID != "" {
			if err := checkNode(ctx, tx, p.TenantID, project, in.TicketID, true); err != nil {
				return err
			}
		}
		for _, node := range in.BlockedNodeIDs {
			if err := checkNode(ctx, tx, p.TenantID, project, node, false); err != nil {
				return err
			}
		}
		if in.SessionID != "" {
			var active bool
			err = tx.QueryRow(ctx, `SELECT stopped_at IS NULL AND archived_at IS NULL FROM harness_sessions WHERE tenant_id=$1 AND project_id=$2 AND id=$3 AND agent_principal_id=$4 FOR SHARE`, p.TenantID, project, in.SessionID, p.ID).Scan(&active)
			if err != nil {
				return err
			}
			if !active {
				return fail(409, "session_ended", "the original session has ended")
			}
		}
		if in.SourceRequestID != "" {
			var owned bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_compat_messages WHERE tenant_id=$1 AND project_id=$2 AND id=$3 AND sender_principal_id=$4 AND sender_session_id IS NOT DISTINCT FROM $5::uuid)`, p.TenantID, project, in.SourceRequestID, p.ID, nullable(in.SessionID)).Scan(&owned)
			if err != nil {
				return err
			}
			if !owned {
				return missing()
			}
		}
		if err := writeCapability(ctx, tx); err != nil {
			return err
		}
		id, err = createNode(ctx, tx, p, project, "question")
		if err != nil {
			return err
		}
		stamp, why := suggestion(in)
		if _, err = tx.Exec(ctx, `INSERT INTO desk_questions(tenant_id,project_id,node_id,input,suggested_outcome,suggestion_reason) VALUES($1,$2,$3,$4,$5,$6)`, p.TenantID, project, id, body, stamp, why); err != nil {
			return err
		}
		dest := in.TicketID
		if dest == "" {
			dest = id
		}
		_, err = tx.Exec(ctx, `INSERT INTO desk_askers(tenant_id,project_id,question_id,principal_id,request_id,request_digest,session_id,source_request_id,ticket_id,comment_node_id,input)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, p.TenantID, project, id, p.ID, in.RequestID, hash, nullable(in.SessionID), nullable(in.SourceRequestID), nullable(in.TicketID), dest, body)
		if err != nil {
			return err
		}
		if err := event(ctx, tx, p, id, "question.asked", 1); err != nil {
			return err
		}
		q, err = m.read(ctx, tx, p, id)
		return err
	})
	return q, replay, err
}
func (m *Module) get(ctx context.Context, p tenant.Principal, id string) (Question, error) {
	var q Question
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error { var err error; q, err = m.read(ctx, tx, p, id); return err })
	return q, err
}
func (m *Module) read(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (Question, error) {
	check, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return Question{}, err
	}
	return m.readWithCheck(ctx, tx, p, id, check)
}
func (m *Module) readWithCheck(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, check authz.ProjectCheck) (Question, error) {
	q := Question{Askers: []Asker{}, Pending: []Pending{}}
	err := tx.QueryRow(ctx, `SELECT q.node_id::text,q.project_id::text,q.revision,q.state,q.input,q.suggested_outcome,q.suggestion_reason,q.created_at,q.updated_at
 FROM desk_questions q JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.node_id
 JOIN nodes project ON project.tenant_id=q.tenant_id AND project.id=q.project_id
 WHERE q.tenant_id=$1 AND q.node_id=$2 AND n.deleted_at IS NULL AND project.deleted_at IS NULL FOR SHARE OF q`, p.TenantID, id).Scan(&q.ID, &q.ProjectID, &q.Revision, &q.State, &q.Input, &q.SuggestedOutcome, &q.SuggestionReason, &q.CreatedAt, &q.UpdatedAt)
	if err != nil {
		return q, err
	}
	if !check("questions.read", q.ProjectID) {
		return q, missing()
	}
	rows, err := tx.Query(ctx, `SELECT id::text,principal_id::text,request_id::text,coalesce(session_id::text,''),reply_root_id::text,comment_node_id::text,created_at,input FROM desk_askers
 WHERE tenant_id=$1 AND question_id=$2 AND ($3 OR principal_id=$4) ORDER BY created_at,id`, p.TenantID, id, p.Kind == tenant.Person, p.ID)
	if err != nil {
		return q, err
	}
	for rows.Next() {
		var a Asker
		if err := rows.Scan(&a.ID, &a.PrincipalID, &a.RequestID, &a.SessionID, &a.ReplyRootID, &a.CommentNodeID, &a.CreatedAt, &a.Input); err != nil {
			rows.Close()
			return q, err
		}
		q.Askers = append(q.Askers, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return q, err
	}
	if len(q.Askers) == 0 {
		return q, missing()
	}
	if p.Kind == tenant.Agent {
		q.Input = q.Askers[0].Input
		q.SuggestedOutcome, q.SuggestionReason = suggestion(q.Input)
	}
	if q.Revision > 1 {
		a := &Answer{}
		err = tx.QueryRow(ctx, `SELECT node_id::text,revision,option_id,answer,reason,outcome,decided_by::text,created_at,deliver_after,coalesce(replaces::text,'') FROM desk_answers WHERE tenant_id=$1 AND question_id=$2 AND revision=$3`, p.TenantID, id, q.Revision).Scan(&a.ID, &a.Revision, &a.OptionID, &a.Answer, &a.Reason, &a.Outcome, &a.DecidedBy, &a.CreatedAt, &a.DeliverAfter, &a.Replaces)
		if err != nil {
			return q, err
		}
		q.Answer = a
	}
	rows, err = tx.Query(ctx, `SELECT e.id::text,coalesce(e.asker_id::text,''),e.revision,e.kind,e.state,e.deliver_after,e.effect_ref,e.error_code,e.error_message,e.effect_data,coalesce(r.state,''),coalesce(r.failure_reason,''),coalesce(e.delivery_session_id::text,''),coalesce(proposal.data->>'state','') FROM desk_pending e LEFT JOIN inbox_receipts r ON r.tenant_id=e.tenant_id AND r.message_id=(CASE WHEN e.kind='inbox' THEN nullif(e.effect_ref,'')::uuid END)
 LEFT JOIN doctrine_proposals proposal ON proposal.tenant_id=e.tenant_id AND proposal.id=nullif(e.effect_data->>'doctrine_id','')::uuid
 WHERE e.tenant_id=$1 AND e.question_id=$2 AND e.revision=$3 AND ($4 OR e.kind='outcome' OR e.asker_id IN (SELECT id FROM desk_askers WHERE tenant_id=$1 AND principal_id=$5)) ORDER BY e.kind,e.id`, p.TenantID, id, q.Revision, p.Kind == tenant.Person, p.ID)
	if err != nil {
		return q, err
	}
	for rows.Next() {
		var e Pending
		if err := rows.Scan(&e.ID, &e.AskerID, &e.Revision, &e.Kind, &e.State, &e.DeliverAfter, &e.EffectRef, &e.ErrorCode, &e.ErrorMessage, &e.EffectData, &e.ReceiptState, &e.ReceiptFailure, &e.DeliverySessionID, &e.DoctrineState); err != nil {
			rows.Close()
			return q, err
		}
		q.Pending = append(q.Pending, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return q, err
	}
	q.Outcomes, err = m.availability(ctx, tx, p, q, q.Input.Doctrine, check)
	return q, err
}
func (m *Module) list(ctx context.Context, p tenant.Principal, project, state string, limit, offset int) (Page, error) {
	out := Page{Items: []Question{}}
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		check, err := authz.ProjectsTx(ctx, tx, p)
		if err != nil {
			return err
		}
		if project != "" {
			if !check("questions.read", project) {
				return missing()
			}
			if err := checkProject(ctx, tx, p, project); err != nil {
				return err
			}
		}
		projects := []string{}
		rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1 AND k.slug='project' AND n.deleted_at IS NULL AND ($2='' OR n.id::text=$2)`, p.TenantID, project)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if check("questions.read", id) {
				projects = append(projects, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT q.node_id::text FROM desk_questions q JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.node_id
 WHERE q.tenant_id=$1 AND q.project_id=ANY($2::uuid[]) AND n.deleted_at IS NULL AND ($3='' OR q.state=$3)
 AND ($4 OR EXISTS(SELECT 1 FROM desk_askers a WHERE a.tenant_id=q.tenant_id AND a.question_id=q.node_id AND a.principal_id=$5))
 ORDER BY q.created_at,q.node_id LIMIT $6 OFFSET $7`, p.TenantID, projects, state, p.Kind == tenant.Person, p.ID, limit+1, offset)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		out.HasMore = len(ids) > limit
		if out.HasMore {
			ids = ids[:limit]
		}
		for _, id := range ids {
			q, err := m.readWithCheck(ctx, tx, p, id, check)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, q)
		}
		return nil
	})
	return out, err
}
func (m *Module) decide(ctx context.Context, p tenant.Principal, id string, in DecisionInput) (Question, error) {
	var q Question
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		q, err = m.decideTx(ctx, tx, p, id, in)
		return err
	})
	return q, err
}
func (m *Module) decideTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, in DecisionInput) (Question, error) {
	var q Question
	if p.Kind != tenant.Person {
		return q, fail(403, "person_required", "a signed-in person must decide")
	}
	hash, _, err := digest(in)
	if err != nil {
		return q, err
	}
	err = func() error {
		if err := treeLock(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,55))`, p.TenantID); err != nil {
			return err
		}
		var project string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM desk_questions WHERE tenant_id=$1 AND node_id=$2 FOR UPDATE`, p.TenantID, id).Scan(&project); err != nil {
			return err
		}
		if err := permit(ctx, tx, p, project, "questions.decide"); err != nil {
			return err
		}
		check, err := authz.ProjectsTx(ctx, tx, p)
		if err != nil {
			return err
		}
		q, err = m.readWithCheck(ctx, tx, p, id, check)
		if err != nil {
			return err
		}
		var previous string
		err = tx.QueryRow(ctx, `SELECT request_digest FROM desk_answers WHERE tenant_id=$1 AND question_id=$2 AND decided_by=$3 AND request_id=$4`, p.TenantID, id, p.ID, in.RequestID).Scan(&previous)
		if err == nil {
			if hash != previous {
				return fail(409, "replay_conflict", "request_id already identifies a different answer")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if q.Revision != in.ExpectedRevision {
			return fail(409, "revision_conflict", "question revision changed")
		}
		answer := in.Answer
		if in.OptionID != "" {
			found := false
			for _, o := range q.Input.Options {
				if o.ID == in.OptionID {
					found = true
					if answer == "" {
						answer = o.Answer
					}
				}
			}
			if !found {
				return fail(400, "invalid_request", "option_id must name a question option")
			}
		}
		target := in.Doctrine
		if target == nil {
			target = q.Input.Doctrine
		}
		available, err := m.availability(ctx, tx, p, q, target, check)
		if err != nil {
			return err
		}
		for _, stamp := range available {
			if stamp.Outcome == in.Outcome && !stamp.Available {
				return fail(422, "outcome_unavailable", stamp.Why)
			}
		}
		data := outcomeInput{}
		if in.Outcome == "doctrine" {
			data.Doctrine = target
		}
		if in.Outcome == "requirement" {
			data.TicketID = q.Input.TicketID
			if err := tx.QueryRow(ctx, `SELECT updated_at FROM nodes WHERE tenant_id=$1 AND id=$2 FOR NO KEY UPDATE`, p.TenantID, data.TicketID).Scan(&data.TicketRevision); err != nil {
				return err
			}
			if in.TicketRevision != nil && !in.TicketRevision.Equal(data.TicketRevision) {
				return fail(409, "ticket_revision_conflict", "The ticket changed; review its criteria before deciding.")
			}
		}
		rawData, err := json.Marshal(data)
		if err != nil {
			return err
		}
		var encodedSize int
		if err := tx.QueryRow(ctx, `SELECT octet_length($1::jsonb::text)`, rawData).Scan(&encodedSize); err != nil {
			return err
		}
		if encodedSize > 4096 {
			return fail(422, "outcome_data_too_large", "The encoded outcome mapping exceeds 4096 bytes; shorten the path or TL;DR.")
		}
		if in.Outcome == "doctrine" {
			if err := m.doctrine.CheckDeskTargetTx(ctx, tx, p, doctrineInput(target)); err != nil {
				_, why := doctrine.DeskFailure(err)
				return fail(422, "outcome_unavailable", why)
			}
		}
		var replaces string
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT r.node_id::text FROM desk_answers r WHERE r.tenant_id=$1 AND r.question_id=$2 AND EXISTS(SELECT 1 FROM desk_pending e WHERE e.tenant_id=r.tenant_id AND e.question_id=r.question_id AND e.revision=r.revision AND e.effect_ref<>'' AND e.kind IN ('inbox','comment')) ORDER BY r.revision DESC LIMIT 1),'')`, p.TenantID, id).Scan(&replaces); err != nil {
			return err
		}
		if err := writeCapability(ctx, tx); err != nil {
			return err
		}
		answerID, err := createNode(ctx, tx, p, id, "decision")
		if err != nil {
			return err
		}
		rev := q.Revision + 1
		now, err := m.now(ctx, tx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO desk_answers(tenant_id,project_id,node_id,question_id,revision,request_id,request_digest,decided_by,option_id,answer,reason,outcome,deliver_after,replaces,outcome_data)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, p.TenantID, project, answerID, id, rev, in.RequestID, hash, p.ID, in.OptionID, answer, in.Reason, in.Outcome, now.Add(10*time.Second), nullable(replaces), rawData)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE desk_pending SET state='replaced' WHERE tenant_id=$1 AND question_id=$2 AND state IN ('pending','failed')`, p.TenantID, id); err != nil {
			return err
		}
		retired, err := retireDecisions(ctx, tx, p, id, answerID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE desk_decisions SET state='superseded',superseded_by=$3 WHERE tenant_id=$1 AND question_id=$2 AND state<>'superseded'`, p.TenantID, id, answerID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO desk_decisions(tenant_id,project_id,question_id,revision) VALUES($1,$2,$3,$4)`, p.TenantID, project, id, rev); err != nil {
			return err
		}
		// Each revision-bound effect applies after grace with its own identity and
		// destination; a failed inbox cannot hide a successful comment or outcome.
		if _, err = tx.Exec(ctx, `INSERT INTO desk_pending(tenant_id,project_id,question_id,revision,asker_id,kind,deliver_after)
 SELECT a.tenant_id,a.project_id,a.question_id,$3,a.id,k.kind,r.deliver_after FROM desk_askers a
 JOIN desk_answers r ON r.tenant_id=a.tenant_id AND r.question_id=a.question_id AND r.revision=$3
 CROSS JOIN (VALUES('inbox'),('comment')) k(kind) WHERE a.tenant_id=$1 AND a.question_id=$2
 UNION ALL SELECT tenant_id,project_id,question_id,revision,NULL,'outcome',deliver_after FROM desk_answers WHERE tenant_id=$1 AND question_id=$2 AND revision=$3`, p.TenantID, id, rev); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE desk_questions SET revision=$3,state='answered',updated_at=clock_timestamp() WHERE tenant_id=$1 AND node_id=$2`, p.TenantID, id, rev); err != nil {
			return err
		}
		if err := settleHeld(ctx, tx, p, q, rev); err != nil {
			return err
		}
		for _, change := range retired {
			if _, err := events.Append(ctx, tx, p, change); err != nil {
				return err
			}
		}
		if err := event(ctx, tx, p, id, "question.answered", rev); err != nil {
			return err
		}
		q, err = m.readWithCheck(ctx, tx, p, id, check)
		return err
	}()
	return q, err
}

// Reuse is withdrawn in the decision transaction, independently of the next
// effect. Only the desk-owned active Knowledge record for this question changes.
func retireDecisions(ctx context.Context, tx pgx.Tx, p tenant.Principal, question, answer string) ([]events.Change, error) {
	var id string
	var before, after json.RawMessage
	err := tx.QueryRow(ctx, `SELECT n.id::text,to_jsonb(n) FROM nodes n JOIN desk_decisions d ON d.tenant_id=n.tenant_id AND d.question_id=$2::uuid AND d.effect_ref=n.id::text
 WHERE n.tenant_id=$1 AND d.state='active' AND n.fields->'metadata'->>'question_id'=$2::uuid::text AND n.deleted_at IS NULL FOR NO KEY UPDATE OF n`, p.TenantID, question).Scan(&id, &before)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE nodes SET state='cancelled',fields=jsonb_set(jsonb_set(fields,'{metadata,decision_state}','"superseded"'),'{metadata,superseded_by}',to_jsonb($3::text)),updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE tenant_id=$1 AND id=$2`, p.TenantID, id, answer); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(nodes) FROM nodes WHERE tenant_id=$1 AND id=$2`, p.TenantID, id).Scan(&after); err != nil {
		return nil, err
	}
	return []events.Change{{NodeID: &id, Type: "knowledge.updated", Before: before, After: after, Metadata: json.RawMessage(`{"reason":"Decision Desk supersession"}`)}}, nil
}
