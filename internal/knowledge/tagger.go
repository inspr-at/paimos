// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
)

// taggerLockKey is the session advisory lock that keeps one tagger across
// every paimos serve process sharing one database. It does not overlap the
// inbox sweeper lock.
const taggerLockKey int64 = 0x0288_1e42_4e01

const (
	taggerInterval = 24 * time.Hour
	taggerLookback = 24 * time.Hour
)

// taggerBatch caps one source scan. Tests lower it to prove the cursor does
// not jump to the end of the window.
var taggerBatch = 200

var closedTicketStates = []string{"accepted", "delivered", "done", "cancelled", "canceled", "archived"}

// Tagger nominates method learnings. It never accepts or dismisses one.
type Tagger struct {
	pool *pgxpool.Pool
}

func NewTagger(pool *pgxpool.Pool) *Tagger { return &Tagger{pool: pool} }

// Run tags once, then every 24 hours, until ctx ends.
func (t *Tagger) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := TagOnce(ctx, t.pool); err != nil && ctx.Err() == nil {
			slog.Error("method learning tagger", "err", err)
		}
		timer.Reset(taggerInterval)
	}
}

// TagOnce nominates candidates when this process holds the advisory lock.
// A lock held elsewhere returns 0, nil. A tenant that fails does not advance
// its cursor; other tenants still run. The count is nominations written.
func TagOnce(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, taggerLockKey).Scan(&locked); err != nil || !locked {
		return 0, err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, taggerLockKey); err != nil {
			_ = conn.Conn().Close(unlockCtx)
		}
	}()
	rows, err := pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()
	total := 0
	var first error
	for _, id := range ids {
		n, err := tagTenant(ctx, pool, id)
		total += n
		if err != nil && first == nil {
			first = err
		}
	}
	return total, first
}

func tagTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string) (int, error) {
	var n int
	err := db.InTenant(db.AllProjects(ctx, "method learning tagger"), pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','5s',true)`); err != nil {
			return err
		}
		var ticketsUntil, commentsUntil, verdictsUntil *time.Time
		err := tx.QueryRow(ctx, `SELECT tickets_until, comments_until, verdicts_until FROM method_learning_tag_cursor WHERE tenant_id=$1`, tenantID).Scan(&ticketsUntil, &commentsUntil, &verdictsUntil)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
		if err != nil {
			return err
		}
		var end time.Time
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&end); err != nil {
			return err
		}
		verdictsOK, err := outcomeVerdictsUsable(ctx, tx)
		if err != nil {
			return err
		}
		var verdictsN int
		var verdictsCursor time.Time
		// A missing outcome table must leave verdicts_until NULL. A zero time
		// would be year 1 and look like a finished scan.
		var verdictsAt *time.Time
		if verdictsOK {
			verdictsN, verdictsCursor, err = tagReviewVerdicts(ctx, tx, tenantID, windowStart(verdictsUntil, end), end)
			if err != nil {
				return err
			}
			verdictsAt = &verdictsCursor
		}
		ticketsN, ticketsCursor, err := tagClosedTickets(ctx, tx, tenantID, windowStart(ticketsUntil, end), end)
		if err != nil {
			return err
		}
		commentsN, commentsCursor, err := tagComments(ctx, tx, tenantID, windowStart(commentsUntil, end), end, !verdictsOK)
		if err != nil {
			return err
		}
		n = verdictsN + ticketsN + commentsN
		_, err = tx.Exec(ctx, `INSERT INTO method_learning_tag_cursor (tenant_id, tickets_until, comments_until, verdicts_until)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (tenant_id) DO UPDATE SET
			  tickets_until = EXCLUDED.tickets_until,
			  comments_until = EXCLUDED.comments_until,
			  verdicts_until = CASE WHEN $5 THEN EXCLUDED.verdicts_until ELSE method_learning_tag_cursor.verdicts_until END`,
			tenantID, ticketsCursor, commentsCursor, verdictsAt, verdictsOK)
		return err
	})
	return n, err
}

func windowStart(cursor *time.Time, end time.Time) time.Time {
	if cursor == nil {
		return end.Add(-taggerLookback)
	}
	return *cursor
}

func outcomeVerdictsUsable(ctx context.Context, tx pgx.Tx) (bool, error) {
	var name *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.outcome_events')::text`).Scan(&name); err != nil {
		return false, err
	}
	if name == nil || *name == "" {
		return false, nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	_, err = sp.Exec(ctx, `SELECT kind, project_id, ticket_node_id, payload, recorded_at FROM outcome_events WHERE kind='review_verdict' AND false`)
	if err != nil {
		_ = sp.Rollback(ctx)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "42P01" || pe.Code == "42703") {
			return false, nil
		}
		return false, err
	}
	return true, sp.Commit(ctx)
}

func finishCursor(rows int, last, end time.Time) time.Time {
	if rows >= taggerBatch {
		return last
	}
	return end
}

func tagClosedTickets(ctx context.Context, tx pgx.Tx, tenantID string, start, end time.Time) (int, time.Time, error) {
	rows, err := tx.Query(ctx, `SELECT n.id::text, n.title, n.updated_at, n.project_id::text, coalesce(n.fields, '{}'::jsonb)
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.tenant_id=$1 AND n.deleted_at IS NULL AND k.slug='ticket'
		  AND n.state = ANY($2::text[])
		  AND n.project_id IS NOT NULL
		  AND n.updated_at >= $3 AND n.updated_at <= $4
		  AND NOT EXISTS (
		    SELECT 1 FROM method_learning_decisions d
		    WHERE d.tenant_id=n.tenant_id AND d.source_key='n-'||n.id::text)
		  AND NOT EXISTS (
		    SELECT 1 FROM method_learning_nominations m
		    WHERE m.tenant_id=n.tenant_id AND m.source_key='n-'||n.id::text)
		  AND NOT EXISTS (
		    SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(n.fields->'tags')='array' THEN n.fields->'tags' ELSE '[]'::jsonb END) tag
		    WHERE lower(btrim(CASE WHEN jsonb_typeof(tag)='string' THEN tag #>> '{}' ELSE coalesce(tag->>'name','') END)) = 'process-learning')
		ORDER BY n.updated_at, n.id
		LIMIT $5`, tenantID, closedTicketStates, start, end, taggerBatch)
	if err != nil {
		return 0, time.Time{}, err
	}
	// pgx keeps this connection busy until the scan closes. A write on the
	// same transaction before that returns "conn busy".
	defer rows.Close()
	type closedHit struct {
		id, title, project string
		at                 time.Time
		fields             json.RawMessage
	}
	var hits []closedHit
	var last time.Time
	for rows.Next() {
		var hit closedHit
		if err = rows.Scan(&hit.id, &hit.title, &hit.at, &hit.project, &hit.fields); err != nil {
			return 0, time.Time{}, err
		}
		last = hit.at
		hits = append(hits, hit)
	}
	if err = rows.Err(); err != nil {
		return 0, time.Time{}, err
	}
	rows.Close()
	added := 0
	for _, hit := range hits {
		if hasLearningTag(hit.fields) {
			continue
		}
		excerpt := oneLine(hit.title, false)
		if excerpt == "" {
			continue
		}
		if err = stampLearningTag(ctx, tx, tenantID, hit.id, hit.fields); err != nil {
			return 0, time.Time{}, err
		}
		ok, err := insertNomination(ctx, tx, tenantID, nodeLearningID(hit.id), hit.project, hit.id, nil, "closed_ticket", excerpt)
		if err != nil {
			return 0, time.Time{}, err
		}
		if ok {
			added++
		}
	}
	return added, finishCursor(len(hits), last, end), nil
}

func tagComments(ctx context.Context, tx pgx.Tx, tenantID string, start, end time.Time, scanVerdict bool) (int, time.Time, error) {
	rows, err := tx.Query(ctx, `SELECT c.id::text, c.at, n.id::text, n.project_id::text,
		  coalesce(latest.after->>'body_markdown', c.after->>'body_markdown', '')
		FROM events c
		JOIN nodes n ON n.tenant_id=c.tenant_id AND n.id=c.node_id AND n.deleted_at IS NULL
		LEFT JOIN LATERAL (
		    SELECT after FROM events e
		    WHERE e.tenant_id=c.tenant_id AND e.node_id=c.node_id
		      AND e.type IN ('comment.updated','comment.deleted')
		      AND e.after->>'comment_id'=c.id::text
		    ORDER BY e.id DESC LIMIT 1
		) latest ON true
		WHERE c.tenant_id=$1 AND c.type='comment.created' AND n.project_id IS NOT NULL
		  AND c.at >= $2 AND c.at <= $3
		  AND coalesce(latest.after->>'deleted','false') <> 'true'
		  AND coalesce(latest.after->>'body_markdown', c.after->>'body_markdown', '') !~* '(^|[^A-Za-z0-9_-])#?process-learning([^A-Za-z0-9_-]|$)'
		  AND (
		    coalesce(latest.after->>'body_markdown', c.after->>'body_markdown', '') ILIKE '%incident%'
		    OR ($4::bool AND coalesce(latest.after->>'body_markdown', c.after->>'body_markdown', '') LIKE '%VERDICT%')
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM method_learning_nominations m
		    WHERE m.tenant_id=c.tenant_id AND m.source_key='c-'||n.id::text||'-'||c.id::text)
		  AND NOT EXISTS (
		    SELECT 1 FROM method_learning_decisions d
		    WHERE d.tenant_id=c.tenant_id AND d.source_key='c-'||n.id::text||'-'||c.id::text)
		ORDER BY c.at, c.id
		LIMIT $5`, tenantID, start, end, scanVerdict, taggerBatch)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer rows.Close()
	type commentHit struct {
		commentID, nodeID, project, body string
		at                               time.Time
	}
	var hits []commentHit
	var last time.Time
	for rows.Next() {
		var hit commentHit
		if err = rows.Scan(&hit.commentID, &hit.at, &hit.nodeID, &hit.project, &hit.body); err != nil {
			return 0, time.Time{}, err
		}
		last = hit.at
		hits = append(hits, hit)
	}
	if err = rows.Err(); err != nil {
		return 0, time.Time{}, err
	}
	rows.Close()
	added := 0
	for _, hit := range hits {
		if processLearningToken.MatchString(hit.body) {
			continue
		}
		origin := ""
		if scanVerdict && strings.Contains(hit.body, "VERDICT") {
			origin = "review_verdict"
		}
		if origin == "" && incidentWord.MatchString(hit.body) {
			origin = "incident_comment"
		}
		if origin == "" {
			continue
		}
		excerpt := oneLine(hit.body, false)
		if excerpt == "" {
			continue
		}
		comment, err := strconv.ParseInt(hit.commentID, 10, 64)
		if err != nil {
			continue
		}
		ok, err := insertNomination(ctx, tx, tenantID, commentLearningID(hit.nodeID, hit.commentID), hit.project, hit.nodeID, &comment, origin, excerpt)
		if err != nil {
			return 0, time.Time{}, err
		}
		if ok {
			added++
		}
	}
	return added, finishCursor(len(hits), last, end), nil
}

func tagReviewVerdicts(ctx context.Context, tx pgx.Tx, tenantID string, start, end time.Time) (int, time.Time, error) {
	rows, err := tx.Query(ctx, `SELECT ticket_node_id::text, payload, recorded_at
		FROM outcome_events
		WHERE tenant_id=$1 AND kind='review_verdict'
		  AND recorded_at >= $2 AND recorded_at <= $3
		ORDER BY recorded_at, id
		LIMIT $4`, tenantID, start, end, taggerBatch)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer rows.Close()
	type verdictHit struct {
		nodeID  string
		payload json.RawMessage
		at      time.Time
	}
	var hits []verdictHit
	var last time.Time
	for rows.Next() {
		var hit verdictHit
		if err = rows.Scan(&hit.nodeID, &hit.payload, &hit.at); err != nil {
			return 0, time.Time{}, err
		}
		last = hit.at
		hits = append(hits, hit)
	}
	if err = rows.Err(); err != nil {
		return 0, time.Time{}, err
	}
	rows.Close()
	added := 0
	for _, hit := range hits {
		ok, err := nominateVerdict(ctx, tx, tenantID, hit.nodeID, hit.payload)
		if err != nil {
			return 0, time.Time{}, err
		}
		if ok {
			added++
		}
	}
	return added, finishCursor(len(hits), last, end), nil
}

func nominateVerdict(ctx context.Context, tx pgx.Tx, tenantID, nodeID string, payload json.RawMessage) (bool, error) {
	var body struct {
		Verdict string `json:"verdict"`
		Summary string `json:"summary"`
	}
	if json.Unmarshal(payload, &body) != nil || (body.Verdict != "pass" && body.Verdict != "fail") {
		return false, nil
	}
	var title, project, kind string
	var fields json.RawMessage
	err := tx.QueryRow(ctx, `SELECT n.title, n.project_id::text, k.slug, coalesce(n.fields, '{}'::jsonb)
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL AND n.project_id IS NOT NULL`, tenantID, nodeID).Scan(&title, &project, &kind, &fields)
	if errors.Is(err, pgx.ErrNoRows) || !issueKinds[kind] {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var decided string
	err = tx.QueryRow(ctx, `SELECT decision FROM method_learning_decisions WHERE tenant_id=$1 AND source_key=$2`, tenantID, nodeLearningID(nodeID)).Scan(&decided)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	excerpt := oneLine(body.Summary, false)
	if excerpt == "" {
		excerpt = oneLine("Review "+body.Verdict+" on "+title, false)
	}
	if excerpt == "" {
		return false, nil
	}
	if err = stampLearningTag(ctx, tx, tenantID, nodeID, fields); err != nil {
		return false, err
	}
	return insertNomination(ctx, tx, tenantID, nodeLearningID(nodeID), project, nodeID, nil, "review_verdict", excerpt)
}

func stampLearningTag(ctx context.Context, tx pgx.Tx, tenantID, nodeID string, fields json.RawMessage) error {
	next, changed, err := withLearningTag(fields)
	if err != nil || !changed {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE nodes SET fields=$3::jsonb WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, nodeID, next)
	return err
}

func withLearningTag(fields json.RawMessage) (json.RawMessage, bool, error) {
	if hasLearningTag(fields) {
		return fields, false, nil
	}
	doc := map[string]any{}
	if len(fields) > 0 && string(fields) != "null" {
		if err := json.Unmarshal(fields, &doc); err != nil {
			return nil, false, err
		}
		if doc == nil {
			doc = map[string]any{}
		}
	}
	switch raw := doc["tags"].(type) {
	case nil:
		doc["tags"] = []any{processLearningTag}
	case []any:
		doc["tags"] = append(raw, processLearningTag)
	default:
		doc["tags"] = []any{raw, processLearningTag}
	}
	out, err := json.Marshal(doc)
	return out, err == nil, err
}

func insertNomination(ctx context.Context, tx pgx.Tx, tenantID, sourceKey, projectID, nodeID string, commentID *int64, origin, excerpt string) (bool, error) {
	excerpt = clipBytes(excerpt, 240)
	if excerpt == "" {
		return false, nil
	}
	// char_length is runes. clipBytes is bytes, and oneLine is already ≤240 runes.
	tag, err := tx.Exec(ctx, `INSERT INTO method_learning_nominations
		(tenant_id, source_key, project_id, node_id, comment_id, origin, excerpt)
		VALUES ($1, $2, $3::uuid, $4::uuid, $5::bigint, $6, $7)
		ON CONFLICT (tenant_id, source_key) DO UPDATE SET
		  origin = EXCLUDED.origin,
		  excerpt = EXCLUDED.excerpt,
		  nominated_at = clock_timestamp()
		WHERE method_learning_nominations.origin IS DISTINCT FROM 'review_verdict'
		  AND EXCLUDED.origin = 'review_verdict'`,
		tenantID, sourceKey, projectID, nodeID, commentID, origin, excerpt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
