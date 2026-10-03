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

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/systemactor"
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

// tagCursor is where one source scan resumes: the (timestamp, id) of the last
// row read, exclusive. A nil id means every row at or before at was read.
type tagCursor struct {
	at *time.Time
	id *string
}

func (c tagCursor) start(end time.Time) (time.Time, *string) {
	if c.at == nil {
		return end.Add(-taggerLookback), nil
	}
	return *c.at, c.id
}

// tagRun counts one tenant pass. skipped is candidates that looked like they
// held a credential; only the count is ever logged.
type tagRun struct {
	added, skipped int
}

// taggerBeforeStamp runs just before a ticket's tag is merged. Tests use it
// to change the ticket between the scan and the write.
var taggerBeforeStamp func(nodeID string)

func tagTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string) (int, error) {
	return retryTagTenant(ctx, func() (int, error) {
		return tagTenantPass(ctx, pool, tenantID)
	})
}

// Retry the whole tenant transaction, including its cursor, after a conflict.
// db.InTenant has rolled back before the backoff, so no locks or partial tags
// survive into the next attempt. Other errors retain their original meaning.
func retryTagTenant(ctx context.Context, pass func() (int, error)) (int, error) {
	const attempts = 3
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, err := pass()
		if err == nil {
			return n, nil
		}
		var pe *pgconn.PgError
		if attempt == attempts-1 || !errors.As(err, &pe) || (pe.Code != "40P01" && pe.Code != "40001") {
			return 0, err
		}
		timer := time.NewTimer((50 * time.Millisecond) << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
}

func tagTenantPass(ctx context.Context, pool *pgxpool.Pool, tenantID string) (int, error) {
	var run tagRun
	err := db.InTenant(db.AllProjects(ctx, "method learning tagger"), pool, tenantID, func(tx pgx.Tx) error {
		run = tagRun{}
		// Match delete/updateNode and queue writers: tenant, tree, pairing, then
		// rows. Node UPDATE triggers also take this tree lock; taking a node
		// row first can deadlock with the status autopilot's startup pass.
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0))`); err != nil {
			return err
		}
		// Coordination locks wait like other node writers. Cap only the later
		// row-lock waits, so a long autopilot batch cannot end this daily pass.
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','5s',true)`); err != nil {
			return err
		}
		var tickets, comments, verdicts tagCursor
		err := tx.QueryRow(ctx, `SELECT tickets_until, tickets_after_id, comments_until, comments_after_id, verdicts_until, verdicts_after_id
			FROM method_learning_tag_cursor WHERE tenant_id=$1`, tenantID).Scan(
			&tickets.at, &tickets.id, &comments.at, &comments.id, &verdicts.at, &verdicts.id)
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
		// A missing outcome table must leave verdicts_until NULL. A zero time
		// would be year 1 and look like a finished scan.
		if verdictsOK {
			if verdicts, err = tagReviewVerdicts(ctx, tx, tenantID, verdicts, end, &run); err != nil {
				return err
			}
		}
		if tickets, err = tagClosedTickets(ctx, tx, tenantID, tickets, end, &run); err != nil {
			return err
		}
		if comments, err = tagComments(ctx, tx, tenantID, comments, end, !verdictsOK, &run); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO method_learning_tag_cursor
			(tenant_id, tickets_until, tickets_after_id, comments_until, comments_after_id, verdicts_until, verdicts_after_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (tenant_id) DO UPDATE SET
			  tickets_until = EXCLUDED.tickets_until,
			  tickets_after_id = EXCLUDED.tickets_after_id,
			  comments_until = EXCLUDED.comments_until,
			  comments_after_id = EXCLUDED.comments_after_id,
			  verdicts_until = CASE WHEN $8 THEN EXCLUDED.verdicts_until ELSE method_learning_tag_cursor.verdicts_until END,
			  verdicts_after_id = CASE WHEN $8 THEN EXCLUDED.verdicts_after_id ELSE method_learning_tag_cursor.verdicts_after_id END`,
			tenantID, tickets.at, tickets.id, comments.at, comments.id, verdicts.at, verdicts.id, verdictsOK)
		return err
	})
	if err != nil {
		return 0, err
	}
	if run.skipped > 0 {
		// The count only. The skipped text may be a credential.
		slog.Info("method learning tagger skipped candidates that look like credentials", "tenant_id", tenantID, "skipped", run.skipped)
	}
	return run.added, err
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
	_, err = sp.Exec(ctx, `SELECT id, kind, project_id, ticket_node_id, payload, recorded_at FROM outcome_events WHERE kind='review_verdict' AND false`)
	if err != nil {
		_ = sp.Rollback(ctx)
		if missingOutcomeSchema(err) {
			return false, nil
		}
		return false, err
	}
	return true, sp.Commit(ctx)
}

func missingOutcomeSchema(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "42P01" || pe.Code == "42703")
}

// nextCursor is the last row read when the batch was full, else the end of
// the window.
func nextCursor(rows int, lastAt time.Time, lastID string, end time.Time) tagCursor {
	if rows >= taggerBatch {
		return tagCursor{at: &lastAt, id: &lastID}
	}
	return tagCursor{at: &end}
}

func tagClosedTickets(ctx context.Context, tx pgx.Tx, tenantID string, cursor tagCursor, end time.Time, run *tagRun) (tagCursor, error) {
	start, after := cursor.start(end)
	rows, err := tx.Query(ctx, `SELECT n.id::text, n.title, n.updated_at, n.project_id::text
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.tenant_id=$1 AND n.deleted_at IS NULL AND k.slug='ticket'
		  AND n.state = ANY($2::text[])
		  AND n.project_id IS NOT NULL
		  AND (($5::uuid IS NULL AND n.updated_at > $3) OR (n.updated_at, n.id) > ($3, $5::uuid))
		  AND n.updated_at <= $4
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
		LIMIT $6`, tenantID, closedTicketStates, start, end, after, taggerBatch)
	if err != nil {
		return cursor, err
	}
	// pgx keeps this connection busy until the scan closes. A write on the
	// same transaction before that returns "conn busy".
	defer rows.Close()
	type closedHit struct {
		id, title, project string
		at                 time.Time
	}
	var hits []closedHit
	for rows.Next() {
		var hit closedHit
		if err = rows.Scan(&hit.id, &hit.title, &hit.at, &hit.project); err != nil {
			return cursor, err
		}
		hits = append(hits, hit)
	}
	if err = rows.Err(); err != nil {
		return cursor, err
	}
	rows.Close()
	for _, hit := range hits {
		if looksSensitive(hit.title) {
			run.skipped++
			continue
		}
		excerpt := oneLine(hit.title, false)
		if excerpt == "" {
			continue
		}
		stamped, err := stampLearningTag(ctx, tx, tenantID, hit.id)
		if err != nil {
			return cursor, err
		}
		if !stamped {
			continue
		}
		ok, err := insertNomination(ctx, tx, tenantID, nominationRow{
			sourceKey: nodeLearningID(hit.id), projectID: hit.project, nodeID: hit.id,
			origin: "closed_ticket", excerpt: excerpt, hash: sourceHash(hit.title),
		})
		if err != nil {
			return cursor, err
		}
		if ok {
			run.added++
		}
	}
	if len(hits) == 0 {
		return nextCursor(0, time.Time{}, "", end), nil
	}
	last := hits[len(hits)-1]
	return nextCursor(len(hits), last.at, last.id, end), nil
}

func tagComments(ctx context.Context, tx pgx.Tx, tenantID string, cursor tagCursor, end time.Time, scanVerdict bool, run *tagRun) (tagCursor, error) {
	start, after := cursor.start(end)
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
		  AND (($5::bigint IS NULL AND c.at > $2) OR (c.at, c.id) > ($2, $5::bigint))
		  AND c.at <= $3
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
		LIMIT $6`, tenantID, start, end, scanVerdict, after, taggerBatch)
	if err != nil {
		return cursor, err
	}
	defer rows.Close()
	type commentHit struct {
		commentID, nodeID, project, body string
		at                               time.Time
	}
	var hits []commentHit
	for rows.Next() {
		var hit commentHit
		if err = rows.Scan(&hit.commentID, &hit.at, &hit.nodeID, &hit.project, &hit.body); err != nil {
			return cursor, err
		}
		hits = append(hits, hit)
	}
	if err = rows.Err(); err != nil {
		return cursor, err
	}
	rows.Close()
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
		// The whole body, not only the excerpt: the inbox re-derives its
		// text from the live comment.
		if looksSensitive(hit.body) {
			run.skipped++
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
		ok, err := insertNomination(ctx, tx, tenantID, nominationRow{
			sourceKey: commentLearningID(hit.nodeID, hit.commentID), projectID: hit.project, nodeID: hit.nodeID,
			commentID: &comment, origin: origin, excerpt: excerpt, hash: sourceHash(hit.body),
		})
		if err != nil {
			return cursor, err
		}
		if ok {
			run.added++
		}
	}
	if len(hits) == 0 {
		return nextCursor(0, time.Time{}, "", end), nil
	}
	last := hits[len(hits)-1]
	return nextCursor(len(hits), last.at, last.commentID, end), nil
}

func tagReviewVerdicts(ctx context.Context, tx pgx.Tx, tenantID string, cursor tagCursor, end time.Time, run *tagRun) (tagCursor, error) {
	start, after := cursor.start(end)
	// The id's type belongs to the outcome table; its text form orders and
	// resumes the scan the same way whatever that type is.
	rows, err := tx.Query(ctx, `SELECT id::text, project_id::text, ticket_node_id::text, payload, recorded_at
		FROM outcome_events
		WHERE tenant_id=$1 AND kind='review_verdict'
		  AND (($4::text IS NULL AND recorded_at > $2) OR (recorded_at, id::text) > ($2, $4::text))
		  AND recorded_at <= $3
		ORDER BY recorded_at, id::text
		LIMIT $5`, tenantID, start, end, after, taggerBatch)
	if err != nil {
		return cursor, err
	}
	defer rows.Close()
	var hits []outcomeVerdict
	for rows.Next() {
		var hit outcomeVerdict
		if err = rows.Scan(&hit.id, &hit.projectID, &hit.nodeID, &hit.payload, &hit.at); err != nil {
			return cursor, err
		}
		hits = append(hits, hit)
	}
	if err = rows.Err(); err != nil {
		return cursor, err
	}
	rows.Close()
	for _, hit := range hits {
		ok, err := nominateVerdict(ctx, tx, tenantID, hit, run)
		if err != nil {
			return cursor, err
		}
		if ok {
			run.added++
		}
	}
	if len(hits) == 0 {
		return nextCursor(0, time.Time{}, "", end), nil
	}
	last := hits[len(hits)-1]
	return nextCursor(len(hits), last.at, last.id, end), nil
}

type outcomeVerdict struct {
	id, projectID, nodeID string
	payload               json.RawMessage
	at                    time.Time
}

// verdictText is a review verdict's inbox text and the material it came from.
// ok is false for a payload that is not a pass or fail verdict.
func verdictText(payload json.RawMessage, title string) (excerpt, material string, ok bool) {
	var body struct {
		Verdict string `json:"verdict"`
		Summary string `json:"summary"`
	}
	if json.Unmarshal(payload, &body) != nil || (body.Verdict != "pass" && body.Verdict != "fail") {
		return "", "", false
	}
	if excerpt = oneLine(body.Summary, false); excerpt != "" {
		return excerpt, body.Verdict + "\x00" + body.Summary, true
	}
	excerpt = oneLine("Review "+body.Verdict+" on "+title, false)
	return excerpt, body.Verdict + "\x00\x00" + title, excerpt != ""
}

func nominateVerdict(ctx context.Context, tx pgx.Tx, tenantID string, hit outcomeVerdict, run *tagRun) (bool, error) {
	var title, project, kind string
	err := tx.QueryRow(ctx, `SELECT n.title, n.project_id::text, k.slug
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL AND n.project_id IS NOT NULL`, tenantID, hit.nodeID).Scan(&title, &project, &kind)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !issueKinds[kind]) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A verdict speaks for the project it was recorded in. A ticket that has
	// since moved elsewhere is not tagged in its new project on its strength.
	if hit.projectID != project {
		return false, nil
	}
	var decided string
	err = tx.QueryRow(ctx, `SELECT decision FROM method_learning_decisions WHERE tenant_id=$1 AND source_key=$2`, tenantID, nodeLearningID(hit.nodeID)).Scan(&decided)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	excerpt, material, ok := verdictText(hit.payload, title)
	if !ok {
		return false, nil
	}
	if looksSensitive(material) {
		run.skipped++
		return false, nil
	}
	stamped, err := stampLearningTag(ctx, tx, tenantID, hit.nodeID)
	if err != nil || !stamped {
		return false, err
	}
	outcome := hit.id
	return insertNomination(ctx, tx, tenantID, nominationRow{
		sourceKey: nodeLearningID(hit.nodeID), projectID: hit.projectID, nodeID: hit.nodeID,
		outcomeID: &outcome, origin: "review_verdict", excerpt: excerpt, hash: sourceHash(material),
	})
}

// stampLearningTag merges the process-learning tag into the ticket's fields
// as they are now. The row is re-read under a lock, so a change committed
// after the scan (priority, assignee, other tags) is kept, and updated_at
// moves forward like any other write, so a person's stale save conflicts
// instead of dropping the tag. A real change appends node.updated in this
// same transaction, authored by the tenant System principal. false means the
// ticket is gone or its fields are not an object; nothing is nominated then.
func stampLearningTag(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) (bool, error) {
	if taggerBeforeStamp != nil {
		taggerBeforeStamp(nodeID)
	}
	before, err := scanSnap(tx.QueryRow(ctx, `SELECT `+nodeReturning+` FROM nodes
		WHERE tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, tenantID, nodeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	next, changed, err := withLearningTag(before.Fields)
	if err != nil {
		return false, nil
	}
	if !changed {
		return true, nil
	}
	actor, err := systemactor.Ensure(ctx, tx, tenantID)
	if err != nil {
		return false, err
	}
	after, err := scanSnap(tx.QueryRow(ctx, `UPDATE nodes SET fields=$3::jsonb, updated_at=`+bumpUpdated+`
		WHERE tenant_id=$1 AND id=$2::uuid RETURNING `+nodeReturning, tenantID, nodeID, next))
	if err != nil {
		return false, err
	}
	meta, err := json.Marshal(map[string]string{"job": "learning-tagger", "reason": "method learning tagger"})
	if err != nil {
		return false, err
	}
	_, err = events.Append(ctx, tx, actor, events.Change{NodeID: &after.ID, Type: "node.updated", Before: before, After: after, Metadata: meta})
	return err == nil, err
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

type nominationRow struct {
	sourceKey, projectID, nodeID string
	commentID                    *int64
	outcomeID                    *string
	origin, excerpt, hash        string
}

func insertNomination(ctx context.Context, tx pgx.Tx, tenantID string, row nominationRow) (bool, error) {
	excerpt := clipBytes(row.excerpt, 240)
	if excerpt == "" {
		return false, nil
	}
	// char_length is runes. clipBytes is bytes, and oneLine is already ≤240 runes.
	// A later review verdict replaces an earlier nomination of the same ticket,
	// with its own project, outcome id and source hash.
	tag, err := tx.Exec(ctx, `INSERT INTO method_learning_nominations
		(tenant_id, source_key, project_id, node_id, comment_id, outcome_id, origin, excerpt, source_hash)
		VALUES ($1, $2, $3::uuid, $4::uuid, $5::bigint, $6, $7, $8, $9)
		ON CONFLICT (tenant_id, source_key) DO UPDATE SET
		  project_id = EXCLUDED.project_id,
		  outcome_id = EXCLUDED.outcome_id,
		  origin = EXCLUDED.origin,
		  excerpt = EXCLUDED.excerpt,
		  source_hash = EXCLUDED.source_hash,
		  nominated_at = clock_timestamp()
		WHERE EXCLUDED.origin = 'review_verdict'`,
		tenantID, row.sourceKey, row.projectID, row.nodeID, row.commentID, row.outcomeID, row.origin, excerpt, row.hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
