// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Bound fan-out and the full human desk projection. A full item stops accepting
// members; the next request creates another item with the same fingerprint.
const maxAskers = 100

type questionMatch struct {
	questionID string
	decisionID string
	revision   int64
}

// The caller holds tenant -> tree locks and has checked ask/read permissions.
// Exact fingerprints include ticket scope, context and every option field. An
// absent fingerprint (anyway, source-linked, requirement/doctrine) cannot match.
func matchQuestion(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, body []byte) (questionMatch, error) {
	var fingerprint *string
	if err := tx.QueryRow(ctx, `SELECT aeon_desk_fingerprint($1::jsonb)`, body).Scan(&fingerprint); err != nil || fingerprint == nil {
		return questionMatch{}, err
	}
	// Two active records are ambiguous, even if the text looks similar. Limit
	// before decoding; do not pick an arbitrary approved answer.
	rows, err := tx.Query(ctx, `SELECT q.node_id::text FROM desk_questions q
 JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.node_id AND n.deleted_at IS NULL
 JOIN desk_decisions d ON d.tenant_id=q.tenant_id AND d.question_id=q.node_id AND d.revision=q.revision
 JOIN desk_answers a ON a.tenant_id=d.tenant_id AND a.question_id=d.question_id AND a.revision=d.revision
 JOIN nodes an ON an.tenant_id=a.tenant_id AND an.id=a.node_id AND an.deleted_at IS NULL
 JOIN principals human ON human.tenant_id=a.tenant_id AND human.id=a.decided_by AND human.kind='person'
 WHERE q.tenant_id=$1 AND q.project_id=$2 AND aeon_desk_fingerprint(q.input)=$3 AND q.state='answered'
 AND d.state='active' AND d.superseded_by IS NULL AND a.outcome='always'
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(q.input->'options') o WHERE o->>'id'=a.option_id AND o->>'answer'=a.answer)
 ORDER BY q.node_id LIMIT 2`, p.TenantID, project, *fingerprint)
	if err != nil {
		return questionMatch{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return questionMatch{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return questionMatch{}, err
	}
	if len(ids) == 1 {
		match := questionMatch{questionID: ids[0]}
		// Question first, then decision. A supersession holding the decision row
		// wins before this final predicate is evaluated; reuse never uses the
		// earlier candidate snapshot as authority.
		err := tx.QueryRow(ctx, `SELECT q.revision FROM desk_questions q
 WHERE q.tenant_id=$1 AND q.node_id=$2 AND q.state='answered' AND aeon_desk_fingerprint(q.input)=$3
 AND (SELECT count(*) FROM (SELECT 1 FROM desk_askers s WHERE s.tenant_id=q.tenant_id AND s.question_id=q.node_id LIMIT $4) members)<$4
 FOR NO KEY UPDATE OF q`, p.TenantID, match.questionID, *fingerprint, maxAskers).Scan(&match.revision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return questionMatch{}, err
		}
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT a.node_id::text FROM desk_decisions d
 JOIN desk_answers a ON a.tenant_id=d.tenant_id AND a.question_id=d.question_id AND a.revision=d.revision
 JOIN nodes an ON an.tenant_id=a.tenant_id AND an.id=a.node_id AND an.deleted_at IS NULL
 JOIN principals human ON human.tenant_id=a.tenant_id AND human.id=a.decided_by AND human.kind='person'
 WHERE d.tenant_id=$1 AND d.project_id=$2 AND d.question_id=$3 AND d.revision=$4
 AND d.state='active' AND d.superseded_by IS NULL AND a.outcome='always'
 AND EXISTS(SELECT 1 FROM jsonb_array_elements($5::jsonb->'options') o WHERE o->>'id'=a.option_id AND o->>'answer'=a.answer)
 FOR NO KEY UPDATE OF d`, p.TenantID, project, match.questionID, match.revision, body).Scan(&match.decisionID)
			if err == nil {
				return match, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return questionMatch{}, err
			}
		}
	}
	match := questionMatch{}
	err = tx.QueryRow(ctx, `SELECT q.node_id::text,q.revision FROM desk_questions q
 JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.node_id AND n.deleted_at IS NULL
 WHERE q.tenant_id=$1 AND q.project_id=$2 AND aeon_desk_fingerprint(q.input)=$3 AND q.state='open'
 AND (SELECT count(*) FROM (SELECT 1 FROM desk_askers s WHERE s.tenant_id=q.tenant_id AND s.question_id=q.node_id LIMIT $4) members)<$4
 ORDER BY q.created_at,q.node_id LIMIT 1 FOR NO KEY UPDATE OF q`, p.TenantID, project, *fingerprint, maxAskers).Scan(&match.questionID, &match.revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return questionMatch{}, nil
	}
	return match, err
}

func addAsker(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, id string, in Input, hash string, body []byte, match questionMatch) (string, error) {
	dest := in.TicketID
	if dest == "" {
		dest = id
	}
	var revision any
	if match.decisionID != "" {
		revision = match.revision
	}
	var askerID string
	err := tx.QueryRow(ctx, `INSERT INTO desk_askers(tenant_id,project_id,question_id,principal_id,request_id,request_digest,session_id,source_request_id,ticket_id,comment_node_id,input,reused_decision_id,reused_revision)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id::text`, p.TenantID, project, id, p.ID, in.RequestID, hash, nullable(in.SessionID), nullable(in.SourceRequestID), nullable(in.TicketID), dest, body, nullable(match.decisionID), revision).Scan(&askerID)
	return askerID, err
}

func reuseAnswer(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, askerID string, match questionMatch) error {
	// The request-scoped unique membership and this increment commit together.
	// Replays return before matching, so neither they nor outbox retries count.
	tag, err := tx.Exec(ctx, `UPDATE desk_decisions SET reuse_count=reuse_count+1
 WHERE tenant_id=$1 AND question_id=$2 AND revision=$3 AND state='active' AND superseded_by IS NULL`, p.TenantID, match.questionID, match.revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(409, "retryable_conflict", "approved answer changed; retry the same request")
	}
	// Reuse is Q&A only: no outcome/permission/spend/doctrine materialization.
	// The source answer's human deadline stays immutable; these effects are due
	// now, without another grace. P3 consumes the usual per-asker effect IDs.
	_, err = tx.Exec(ctx, `INSERT INTO desk_pending(tenant_id,project_id,question_id,revision,asker_id,kind,deliver_after)
 SELECT $1,$2,$3,$4,$5,k.kind,statement_timestamp() FROM (VALUES('inbox'),('comment')) k(kind)`, p.TenantID, project, match.questionID, match.revision, askerID)
	return err
}
