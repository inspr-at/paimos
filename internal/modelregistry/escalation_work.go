// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// escalationForWork makes existing ticket-aware routing consume tier A. It
// changes only the next build plan; managed starts still prove exit and reserve
// through the shared retry adapter. Review routes retain their exact gate.
func escalationForWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time) (*WorkResolution, error) {
	if q.TicketID == "" || q.Role != "build" && q.Role != "build-hard" {
		return nil, nil
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT state FROM work_escalations WHERE ticket_node_id=$1`, q.TicketID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state struct {
		EpisodeID       string   `json:"episode_id"`
		OriginalProfile string   `json:"original_profile_id"`
		Status          string   `json:"status"`
		UsedProfiles    []string `json:"used_profile_ids"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if state.Status != "stuck" && state.Status != "awaiting_decision" {
		return nil, nil
	}
	if state.Status == "awaiting_decision" {
		return &WorkResolution{Resolution: Resolution{Role: q.Role, Source: "aeon", OwnerRequired: true, Ladder: []Candidate{}}, Trace: PreferenceTrace{Blocked: "stuck work awaits a Decision Desk decision"}}, nil
	}
	excluded, err := EscalationExclusionsTx(ctx, tx, q.TicketID, state.OriginalProfile, state.EpisodeID, state.UsedProfiles)
	if err != nil {
		return nil, err
	}
	out, err := ResolveEscalation(ctx, tx, p, q, excluded, now)
	return &out, err
}

// EscalationOriginalTx captures the episode's original writer once. For legacy
// episodes that already charged a retry, recover the writer from that retry's
// lineage rather than treating its new session as the original.
func EscalationOriginalTx(ctx context.Context, tx pgx.Tx, ticket, episode string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var original *string
	if episode != "" {
		err := tx.QueryRow(ctx, `SELECT (SELECT previous.model_profile_id::text FROM agent_runs r
 JOIN agent_runs previous ON previous.tenant_id=r.tenant_id AND previous.id=r.retry_of_run_id
 JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id
 WHERE (n.parent_id=$1 OR r.queue_node_id=$1) AND r.trace->'escalation'->>'episode_id'=$2
 ORDER BY r.created_at,r.id LIMIT 1)`, ticket, episode).Scan(&original)
		if err != nil {
			return "", err
		}
	}
	if original == nil {
		err := tx.QueryRow(ctx, `SELECT (SELECT model_profile_id::text FROM harness_sessions
 WHERE ticket_node_id=$1 AND role='worker' AND model_profile_id IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM work_orders w WHERE w.node_id=harness_sessions.work_order_id AND w.kind='review')
 ORDER BY created_at DESC,id DESC LIMIT 1)`, ticket).Scan(&original)
		if err != nil {
			return "", err
		}
	}
	if original == nil {
		return "", nil
	}
	return *original, nil
}

// EscalationExclusionsTx is shared by episode planning and ticket-aware routing.
// The persisted original plus at most two charged profiles retain episode
// history; a current worker is also fenced out, including legacy episodes.
func EscalationExclusionsTx(ctx context.Context, tx pgx.Tx, ticket, original, episode string, used []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if len(used) > 2 {
		return nil, fail(409, "escalation history exceeds attempt bound")
	}
	excluded := slices.Clone(used)
	if original == "" {
		var err error
		chargedEpisode := ""
		if len(used) > 0 {
			chargedEpisode = episode
		}
		original, err = EscalationOriginalTx(ctx, tx, ticket, chargedEpisode)
		if err != nil {
			return nil, err
		}
	}
	if original != "" && !slices.Contains(excluded, original) {
		excluded = append(excluded, original)
	}
	current, err := EscalationOriginalTx(ctx, tx, ticket, "")
	if err != nil {
		return nil, err
	}
	if current != "" && !slices.Contains(excluded, current) {
		excluded = append(excluded, current)
	}
	if len(excluded) > 3 {
		return nil, fail(409, "escalation history exceeds attempt bound")
	}
	return excluded, nil
}
