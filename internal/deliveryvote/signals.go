// SPDX-License-Identifier: AGPL-3.0-only

package deliveryvote

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// countTicketEventSignals is the only counter for review rounds, CI failures
// and reverts. The source is ticket events already stored on the session's
// node, not a separate outcome log:
//
//	review rounds — review.verdict, review.round, gate.finding, gate.verdict
//	CI failures — ci.failed, ci.failure, check.failed
//	reverts — an event with undo_of, or change.reverted, node.reverted, delivery.reverted
//
// An event that names session_id counts only for that session. An event that
// names none counts only while the session was running. delivery.rated is not
// a signal. Zero means none of that kind were recorded.
//
// AEON-286 will provide outcome events. Switch this function to those events
// when they land; do not add a second counter at the call site.
func countTicketEventSignals(ctx context.Context, tx pgx.Tx, tenantID string, ids []string) (map[string]Signals, error) {
	// Source: ticket events listed above. AEON-286 outcome events replace this query.
	const signalSQL = `
SELECT s.id::text,
	count(DISTINCT CASE
		WHEN e.type IN ('review.verdict', 'review.round', 'gate.finding', 'gate.verdict')
		THEN coalesce(nullif(e.after->>'round', ''), e.id::text)
	END)::int,
	count(*) FILTER (WHERE e.type IN ('ci.failed', 'ci.failure', 'check.failed'))::int,
	count(*) FILTER (WHERE e.id IS NOT NULL AND (e.undo_of IS NOT NULL OR e.type IN ('change.reverted', 'node.reverted', 'delivery.reverted')))::int
FROM harness_sessions s
LEFT JOIN events e
	ON e.tenant_id = s.tenant_id
	AND e.node_id = s.ticket_node_id
	AND e.type <> 'delivery.rated'
	AND (
		coalesce(e.after->>'session_id', e.before->>'session_id', '') = s.id::text
		OR (
			coalesce(e.after->>'session_id', e.before->>'session_id', '') = ''
			AND e.at >= s.created_at
			AND (s.stopped_at IS NULL OR e.at <= s.stopped_at)
		)
	)
WHERE s.tenant_id = $1::uuid AND s.id = ANY($2::uuid[])
GROUP BY s.id`
	rows, err := tx.Query(ctx, signalSQL, tenantID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Signals{}
	for rows.Next() {
		var id string
		var signal Signals
		if err := rows.Scan(&id, &signal.ReviewRounds, &signal.CIFailures, &signal.Reverts); err != nil {
			return nil, err
		}
		out[id] = signal
	}
	return out, rows.Err()
}
