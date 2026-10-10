// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/deskdelivery"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const (
	dispatchBatch     = 32
	deliveryReconcile = 30 * time.Second
)

// Run wakes on committed events and sleeps to the next database deadline, with
// bounded reconciliation for missed hints. Each effect retains its revision lock;
// replicas, restarts and retries therefore need no in-memory lease authority.
func (m *Module) Run(ctx context.Context) {
	wakes := make(chan struct{}, 1)
	listenCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); m.listenDelivery(listenCtx, wakes, waitDelivery) }()
	defer func() { cancel(); <-done }()
	m.runDelivery(ctx, wakes, time.Now, waitDelivery)
}

func waitDelivery(ctx context.Context, wakes <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wakes:
	case <-timer.C:
	}
	return true
}

// Clock and wait injection establish deadline/wake ordering without sleeps.
func (m *Module) runDelivery(ctx context.Context, wakes <-chan struct{}, now func() time.Time, wait func(context.Context, <-chan struct{}, time.Duration) bool) {
	for ctx.Err() == nil {
		delay := m.dispatchQueue(ctx, now)
		if !wait(ctx, wakes, max(delay, 10*time.Millisecond)) {
			return
		}
	}
}

func (m *Module) dispatchQueue(ctx context.Context, now func() time.Time) time.Duration {
	deadline := now().Add(deliveryReconcile)
	cursor := ""
	for {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		rows, err := m.pool.Query(readCtx, `SELECT id::text FROM tenants WHERE id::text>$1 ORDER BY id::text LIMIT 100`, cursor)
		if err != nil {
			cancel()
			if ctx.Err() == nil {
				slog.Error("desk dispatcher tenant scan failed")
			}
			return min(time.Second, deadline.Sub(now()))
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		rowErr := rows.Err()
		rows.Close()
		cancel()
		if err != nil || rowErr != nil {
			return min(time.Second, deadline.Sub(now()))
		}
		for _, id := range ids {
			started := now()
			batchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			next, e := m.DispatchTenant(batchCtx, id)
			cancel()
			if e != nil && ctx.Err() == nil {
				slog.Error("desk delivery pass failed")
			}
			if candidate := started.Add(next); candidate.Before(deadline) {
				deadline = candidate
			}
		}
		if len(ids) < 100 || ctx.Err() != nil {
			return deadline.Sub(now())
		}
		// Retain earlier pages' deadlines while giving every tenant a bounded pass.
		cursor = ids[len(ids)-1]
	}
}

func wakeDelivery(wakes chan<- struct{}) {
	select {
	case wakes <- struct{}{}:
	default:
	}
}

func (m *Module) listenDelivery(ctx context.Context, wakes chan<- struct{}, wait func(context.Context, <-chan struct{}, time.Duration) bool) {
	for ctx.Err() == nil {
		_ = m.listenDeliveryConnection(ctx, wakes)
		// A disconnect can hide a newly committed deadline. Reconcile immediately,
		// then subscribe before scanning again on reconnect.
		wakeDelivery(wakes)
		if !wait(ctx, nil, time.Second) {
			return
		}
	}
}

func (m *Module) listenDeliveryConnection(ctx context.Context, wakes chan<- struct{}) error {
	ctx, stop := context.WithTimeout(ctx, db.ListenerMaxLifetime)
	defer stop()
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	conn, err := pgx.ConnectConfig(connectCtx, m.pool.Config().ConnConfig.Copy())
	if conn != nil {
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = conn.Close(closeCtx)
		}()
	}
	if err == nil {
		_, err = conn.Exec(connectCtx, "LISTEN aeon_events")
	}
	cancel()
	if err != nil {
		return err
	}
	wakeDelivery(wakes)
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var hint struct {
			Tenant string `json:"tenant_id"`
			ID     int64  `json:"id"`
		}
		if len(n.Payload) > 1024 || json.Unmarshal([]byte(n.Payload), &hint) != nil || !uuidRE.MatchString(hint.Tenant) || hint.ID < 1 {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var relevant bool
		err = db.InTenantReadSnapshot(db.AllProjects(readCtx, "decision desk notification hint; effects reauthorized"), m.pool, hint.Tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(readCtx, `SELECT coalesce(type='question.answered' OR type='inbox.receipt_failed' AND after->>'failure_reason'='session_ended',false) FROM events WHERE tenant_id=$1 AND id=$2`, hint.Tenant, hint.ID).Scan(&relevant)
		})
		cancel()
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if relevant {
			wakeDelivery(wakes)
		}
	}
}

// DispatchTenant consumes bounded due work. The optional clock is shared with
// decisions for deterministic boundary tests, never a request-supplied time.
func (m *Module) DispatchTenant(ctx context.Context, tid string) (time.Duration, error) {
	ctx = db.AllProjects(ctx, "decision desk durable outbox")
	var ids []string
	var due *time.Time
	var now time.Time
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := db.InTenantReadSnapshot(readCtx, m.pool, tid, func(tx pgx.Tx) error {
		var err error
		now, err = m.now(readCtx, tx)
		if err != nil {
			return err
		}
		return tx.QueryRow(readCtx, `WITH candidates AS MATERIALIZED (
 SELECT e.id,e.question_id,e.kind,coalesce(e.retry_at,e.deliver_after) AS ordering,greatest(e.deliver_after,coalesce(e.retry_at,e.deliver_after)) AS due
 FROM desk_pending e JOIN desk_questions q ON q.tenant_id=e.tenant_id AND q.node_id=e.question_id AND q.revision=e.revision
 LEFT JOIN inbox_receipts r ON r.tenant_id=e.tenant_id AND r.message_id=(CASE WHEN e.kind='inbox' THEN nullif(e.effect_ref,'')::uuid END)
 WHERE e.tenant_id=$1 AND e.kind IN ('inbox','comment','outcome') AND (e.state IN ('pending','failed') OR e.state='delivered' AND r.state='failed' AND r.failure_reason='session_ended')
 AND coalesce(e.effect_data->>'retryable','true')<>'false'
 ) SELECT ARRAY(SELECT id::text FROM candidates WHERE due<=$2
 ORDER BY ordering,question_id,(kind='outcome') DESC,kind,id LIMIT $3),
 (SELECT min(due) FROM candidates WHERE due>$2)`, tid, now, dispatchBatch).Scan(&ids, &due)
	})
	cancel()
	if err != nil {
		return time.Second, err
	}
	for _, id := range ids {
		if err = m.dispatchOne(ctx, tid, id); err != nil {
			return time.Second, err
		}
	}
	if len(ids) != 0 {
		// Effects may have persisted retries. Refresh with the same combined read
		// after draining this batch, rather than use a pre-effect deadline.
		return 0, nil
	}
	if due != nil {
		return max(0, due.Sub(now)), nil
	}
	return deliveryReconcile, nil
}

type delivery struct {
	prepared                                                *doctrine.PreparedInbox
	prepareErr                                              error
	doctrineInput                                           doctrine.InboxInput
	id, project, question, asker, kind, state, ref, session string
	revision                                                int64
	due                                                     time.Time
	retry                                                   *time.Time
}

func (m *Module) dispatchOne(ctx context.Context, tid, id string) error {
	prepared, input, prepareErr := m.prepareDoctrineEffect(ctx, tid, id)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','3s',true),set_config('statement_timeout','10s',true)`); err != nil {
			return err
		}
		if err := treeLock(ctx, tx, tid); err != nil {
			return err
		}
		d := delivery{prepared: prepared, doctrineInput: input, prepareErr: prepareErr}
		// Lookup carries no authority. Lock hierarchy before any generation rows.
		if err := tx.QueryRow(ctx, `SELECT project_id::text,question_id::text FROM desk_pending WHERE tenant_id=$1 AND id=$2`, tid, id).Scan(&d.project, &d.question); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, d.project); err != nil {
			return err
		}
		var current int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM desk_questions WHERE tenant_id=$1 AND node_id=$2 FOR NO KEY UPDATE`, tid, d.question).Scan(&current); err != nil {
			return err
		}
		d.id = id
		if err := tx.QueryRow(ctx, `SELECT coalesce(asker_id::text,''),revision,kind,state,deliver_after,retry_at,effect_ref,coalesce(delivery_session_id::text,'') FROM desk_pending WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tid, id).Scan(&d.asker, &d.revision, &d.kind, &d.state, &d.due, &d.retry, &d.ref, &d.session); err != nil {
			return err
		}
		now, err := m.now(ctx, tx)
		if err != nil {
			return err
		}
		var retryable *bool
		if err := tx.QueryRow(ctx, `SELECT (effect_data->>'retryable')::boolean FROM desk_pending WHERE tenant_id=$1 AND id=$2`, tid, id).Scan(&retryable); err != nil {
			return err
		}
		if retryable != nil && !*retryable {
			return nil
		}
		if current != d.revision || d.state == "replaced" || now.Before(d.due) || d.retry != nil && now.Before(*d.retry) {
			return nil
		}
		if d.state == "delivered" {
			var reroute bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_receipts WHERE tenant_id=$1 AND message_id::text=$2 AND state='failed' AND failure_reason='session_ended')`, tid, d.ref).Scan(&reroute); err != nil {
				return err
			}
			if d.kind != "inbox" || !reroute {
				return nil
			}
		}
		if err := writeCapability(ctx, tx); err != nil {
			return err
		}
		// A failed effect rolls back only its own changes; comments and other askers
		// keep progressing. SQL errors never leak payload text into logs or status.
		effect, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		err = m.deliverEffect(ctx, effect, tid, d)
		if err == nil {
			return effect.Commit(ctx)
		}
		if rollbackErr := effect.Rollback(ctx); rollbackErr != nil {
			return rollbackErr
		}
		code, message := "delivery_failed", "The delivery could not be saved; it will be retried."
		if d.kind == "outcome" {
			code, message = "outcome_failed", "The outcome could not be applied; it will be retried."
		}
		var ae *apiError
		if errors.As(err, &ae) {
			code, message = ae.code, ae.message
		}
		retryableEffect := true
		var retryAt any = now.Add(30 * time.Second)
		transientDoctrine := code == "stale_source" || code == "public_main_unavailable"
		if d.kind == "outcome" && ae != nil && !transientDoctrine && (ae.status == 400 || ae.status == 409 || ae.status == 422) {
			retryableEffect = false
			retryAt = nil
			message += " Review this failure and decide again; automatic retries have stopped."
		} else if d.kind == "outcome" && transientDoctrine {
			message += " This temporary doctrine failure will be retried automatically."
		}
		_, err = tx.Exec(ctx, `UPDATE desk_pending SET state='failed',error_code=$3,retry_at=$4,error_message=$5,effect_data=jsonb_set(effect_data,'{retryable}',to_jsonb($6::boolean)) WHERE tenant_id=$1 AND id=$2`, tid, id, code, retryAt, message, retryableEffect)
		return err
	})
}

func (m *Module) deliverEffect(ctx context.Context, tx pgx.Tx, tid string, d delivery) error {
	if d.kind == "outcome" {
		return m.applyOutcome(ctx, tx, tid, d)
	}
	p := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	a := Asker{}
	answer := Answer{Revision: d.revision}
	if err := tx.QueryRow(ctx, `SELECT r.node_id::text,r.answer,r.decided_by::text,coalesce(r.replaces::text,''),p.name FROM desk_answers r JOIN principals p ON p.tenant_id=r.tenant_id AND p.id=r.decided_by AND p.kind='person' WHERE r.tenant_id=$1 AND r.question_id=$2 AND r.revision=$3`, tid, d.question, d.revision).Scan(&answer.ID, &answer.Answer, &p.ID, &answer.Replaces, &p.Name); err != nil {
		return err
	}
	for _, permission := range []string{"questions.read", "questions.decide"} {
		if err := permit(ctx, tx, p, d.project, permission); err != nil {
			return fail(403, "answerer_access_lost", "answerer access lost")
		}
	}
	if err := checkNode(ctx, tx, tid, d.project, d.question, false); err != nil {
		return fail(404, "question_unavailable", "question unavailable")
	}
	if err := checkProject(ctx, tx, p, d.project); err != nil {
		return fail(404, "project_unavailable", "project unavailable")
	}
	if err := tx.QueryRow(ctx, `SELECT id::text,principal_id::text,coalesce(session_id::text,''),reply_root_id::text,comment_node_id::text,input FROM desk_askers WHERE tenant_id=$1 AND id=$2`, tid, d.asker).Scan(&a.ID, &a.PrincipalID, &a.SessionID, &a.ReplyRootID, &a.CommentNodeID, &a.Input); err != nil {
		return err
	}
	if d.kind == "comment" {
		if err := checkNode(ctx, tx, tid, d.project, a.CommentNodeID, false); err != nil {
			return fail(404, "comment_destination_unavailable", "comment destination unavailable")
		}
		// The answer transaction authorized this effect. Recheck current comment
		// authority for ticket destinations; protected question-node comments use
		// questions.decide and expose only the answer, never private ask context.
		if a.CommentNodeID != d.question {
			if err := permit(ctx, tx, p, d.project, "nodes.write"); err != nil {
				return fail(403, "comment_access_lost", "comment access lost")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE desk_pending SET state='delivered',error_code='',retry_at=NULL,effect_ref=$3 WHERE tenant_id=$1 AND id=$2`, tid, d.id, "desk-comment/"+d.id); err != nil {
			return err
		}
		label := "Answer"
		kind := "question.answer"
		if answer.Replaces != "" {
			label = "Correction"
			kind = "question.correction"
		}
		text := fmt.Sprintf("%s to question %s (revision %d, answer %s)", label, d.question, d.revision, answer.ID)
		if answer.Replaces != "" {
			text += "; replaces=" + answer.Replaces
		}
		text += "\n\n" + answer.Answer
		_, err := events.Append(ctx, tx, p, events.Change{NodeID: &a.CommentNodeID, Type: "comment.created", After: map[string]any{"body_markdown": text, "desk_kind": kind, "question_id": d.question, "answer_id": answer.ID, "revision": d.revision, "asker_id": a.ID, "replaces": answer.Replaces, "effect_id": d.id}})
		return err
	}
	recipient := tenant.Principal{ID: a.PrincipalID, TenantID: tid, Scopes: []string{"questions.read"}}
	if err := tx.QueryRow(ctx, `SELECT kind,name FROM principals WHERE tenant_id=$1 AND id=$2`, tid, a.PrincipalID).Scan(&recipient.Kind, &recipient.Name); err != nil {
		return err
	}
	if err := authz.RequireTx(ctx, tx, recipient, "questions.read", authz.Scope{ProjectID: d.project}); err != nil {
		return fail(403, "asker_access_lost", "asker access lost")
	}
	snapshot := deskdelivery.Answer{QuestionID: d.question, AnswerID: answer.ID, Revision: d.revision, Replaces: answer.Replaces}
	session, err := routeAnswer(ctx, tx, tid, d.project, a, snapshot)
	if err != nil {
		return err
	}
	if a.SessionID != "" && session == "" {
		now, err := m.now(ctx, tx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE desk_pending SET state='failed',error_code='successor_pending',retry_at=$3 WHERE tenant_id=$1 AND id=$2`, tid, d.id, now.Add(30*time.Second))
		return err
	}
	if a.PrincipalID == p.ID {
		return fail(409, "inbox_unavailable", "self answers remain on the question")
	}
	messageID := d.id
	if d.ref != "" {
		if session == d.session {
			return fail(409, "inbox_unavailable", "previous inbox delivery failed")
		}
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&messageID); err != nil {
			return err
		}
	}
	replyTo := a.ReplyRootID
	if a.Input.SourceRequestID != "" {
		var sourceOwned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_compat_messages WHERE tenant_id=$1 AND project_id=$2 AND id=$3 AND sender_principal_id=$4 AND recipient_principal_id=$5 AND sender_session_id IS NOT DISTINCT FROM $6::uuid)`, tid, d.project, a.Input.SourceRequestID, a.PrincipalID, p.ID, nullable(a.SessionID)).Scan(&sourceOwned); err != nil {
			return err
		}
		if sourceOwned {
			replyTo = a.Input.SourceRequestID
		}
	}

	// Reserve destination/result before appending any event; event counter last.
	if _, err := tx.Exec(ctx, `UPDATE desk_pending SET state='delivered',effect_ref=$3,error_code='',retry_at=NULL,delivery_session_id=$4 WHERE tenant_id=$1 AND id=$2`, tid, d.id, messageID, nullable(session)); err != nil {
		return err
	}
	kind := "question.answer"
	if answer.Replaces != "" {
		kind = "question.correction"
	}
	body, _ := json.Marshal(map[string]any{"type": kind, "question_id": d.question, "answer_id": answer.ID, "revision": d.revision, "answer": answer.Answer, "replaces": answer.Replaces, "asker_id": a.ID})
	return inbox.RecordDeskReply(ctx, tx, p, inbox.DeskReply{ID: messageID, RootID: a.ReplyRootID, ReplyToID: replyTo, ProjectID: d.project, RecipientID: a.PrincipalID, RecipientSessionID: session, Body: string(body), RootBody: "Decision Desk question " + d.question + "; read through ask status", RootSenderSessionID: a.SessionID, RecipientName: recipient.Name})
}

// routeAnswer serializes against AEON-524 registration using its hierarchy
// lock. It follows only that adapter's reciprocal continuation proof, never
// "the newest session". Missing routes are durable and never wake old workers.
func routeAnswer(ctx context.Context, tx pgx.Tx, tid, project string, a Asker, answer deskdelivery.Answer) (string, error) {
	if a.SessionID == "" {
		return "", nil
	}
	id := a.SessionID
	for range 16 {
		var next string
		var ended bool
		var snapshots []deskdelivery.Answer
		if err := tx.QueryRow(ctx, `SELECT stopped_at IS NOT NULL OR archived_at IS NOT NULL OR coalesce(pause_record->>'state','') IN ('paused','resume_requested','resumed'),coalesce(handed_over_to_id::text,''),desk_answers FROM harness_sessions WHERE tenant_id=$1 AND project_id=$2 AND id=$3 AND agent_principal_id=$4 FOR NO KEY UPDATE`, tid, project, id, a.PrincipalID).Scan(&ended, &next, &snapshots); err != nil {
			return "", fail(409, "session_unavailable", "original generation unavailable")
		}
		if ended || id != a.SessionID {
			found := false
			for i, old := range snapshots {
				if old.QuestionID == answer.QuestionID {
					if old.Revision <= answer.Revision {
						snapshots[i] = answer
					}
					found = true
					break
				}
			}
			if !found {
				if len(snapshots) >= 100 {
					return "", fail(409, "handover_full", "handover answer limit reached")
				}
				snapshots = append(snapshots, answer)
			}
			raw, err := json.Marshal(snapshots)
			if err != nil {
				return "", err
			}
			if _, err = tx.Exec(ctx, `UPDATE harness_sessions SET desk_answers=$2::jsonb,revision=revision+1 WHERE tenant_id=$1 AND id=$3`, tid, raw, id); err != nil {
				return "", err
			}
		}
		if !ended {
			return id, nil
		}
		if next == "" {
			// Persist the handover even though no inbox route exists. Caller treats
			// this as a committed routing wait, not a failed savepoint.
			return "", nil
		}
		var reciprocal bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions n JOIN harness_sessions o ON o.tenant_id=n.tenant_id AND o.project_id=n.project_id AND o.id=$5::uuid WHERE n.tenant_id=$1 AND n.project_id=$2 AND n.id=$3 AND n.agent_principal_id=$4 AND n.harness=o.harness AND n.role=o.role AND (n.continuation_handover->>'succeeds_session_id'=$5::uuid::text OR o.role='coordinator'))`, tid, project, next, a.PrincipalID, id).Scan(&reciprocal); err != nil {
			return "", err
		}
		if !reciprocal {
			return "", fail(409, "successor_unverified", "successor lineage is not verified")
		}
		id = next
	}
	return "", fail(409, "successor_chain_limit", "successor chain exceeds routing bound")
}

// Expensive rendering and the private quote guard run before the mutation
// fence. The exact revision, actor, source and permissions are checked again
// in the final transaction; this snapshot never grants authority.
func (m *Module) prepareDoctrineEffect(ctx context.Context, tid, id string) (*doctrine.PreparedInbox, doctrine.InboxInput, error) {
	if m.doctrine == nil {
		return nil, doctrine.InboxInput{}, nil
	}
	var p tenant.Principal
	var in outcomeInput
	var input Input
	var a Answer
	var ticketKey, questionID string
	p.TenantID, p.Kind = tid, tenant.Person
	err := db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		now, err := m.now(ctx, tx)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT a.node_id::text,a.answer,a.reason,a.decided_by::text,a.outcome_data,q.input,coalesce(n.key,''),q.node_id::text
 FROM desk_pending e JOIN desk_answers a ON a.tenant_id=e.tenant_id AND a.question_id=e.question_id AND a.revision=e.revision
 JOIN desk_questions q ON q.tenant_id=e.tenant_id AND q.node_id=e.question_id AND q.revision=e.revision
 LEFT JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=(q.input->>'ticket_id')::uuid AND n.deleted_at IS NULL
 WHERE e.tenant_id=$1 AND e.id=$2 AND e.kind='outcome' AND a.outcome='doctrine' AND e.state IN ('pending','failed')
 AND e.deliver_after<=$3 AND coalesce(e.retry_at,e.deliver_after)<=$3 AND coalesce(e.effect_data->>'retryable','true')<>'false'`, tid, id, now).Scan(&a.ID, &a.Answer, &a.Reason, &p.ID, &in, &input, &ticketKey, &questionID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, doctrine.InboxInput{}, nil
	}
	if err != nil {
		return nil, doctrine.InboxInput{}, err
	}
	draft := doctrineInput(in.Doctrine)
	draft.RequestID, draft.Source, draft.Why, draft.Ticket = a.ID, a.Answer, a.Reason, ticketKey
	if draft.Why == "" {
		draft.Why = "Human answer recorded on Decision Desk question " + questionID
	}
	prepared, err := m.doctrine.PrepareDeskDraft(ctx, p, draft)
	if err != nil {
		return nil, draft, doctrineOutcomeError(err)
	}
	return prepared, draft, nil
}
