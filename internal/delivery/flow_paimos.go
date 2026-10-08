// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

const (
	flowSyncBatch    = 200
	flowSyncInterval = 10 * time.Second
)

// RunFlow keeps the Flow steps of PAIMOS work in step with the event log:
// build, fix and merge rounds of the work queue, review gates and holds.
// Each pass reads a bounded batch after the tenant's stored cursor; replays
// converge on the same rows.
func (m *Module) RunFlow(ctx context.Context) {
	for ctx.Err() == nil {
		m.flowRound(ctx)
		timer := time.NewTimer(flowSyncInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *Module) flowRound(ctx context.Context) {
	var cursor string
	for ctx.Err() == nil {
		ids := []string{}
		scanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := db.InTenant(db.NoProjects(scanCtx, "delivery flow tenant scan"), m.pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
			rows, err := tx.Query(scanCtx, `SELECT id::text FROM tenants WHERE ($1::uuid IS NULL OR id>$1) ORDER BY id LIMIT 100`, nullable(cursor))
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
			}
			return rows.Err()
		})
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("delivery flow tenant scan incomplete")
			}
			return
		}
		for _, tid := range ids {
			// A bounded number of batches per tenant and round keeps one busy
			// tenant from starving the others; the rest follows next round.
			for i := 0; i < 10 && ctx.Err() == nil; i++ {
				more, err := m.SyncFlow(ctx, tid)
				if err != nil {
					if ctx.Err() == nil {
						slog.Warn("delivery flow sync incomplete", "tenant_id", tid)
					}
					break
				}
				if !more {
					break
				}
			}
		}
		if len(ids) < 100 {
			return
		}
		cursor = ids[len(ids)-1]
	}
}

// flowSourceEvent is one PAIMOS event the projector reads.
type flowSourceEvent struct {
	ID       int64
	Type     string
	NodeID   *string
	At       time.Time
	Before   json.RawMessage
	After    json.RawMessage
	Metadata json.RawMessage
}

// SyncFlow projects one bounded batch of the tenant's PAIMOS delivery events
// into Flow steps and reports whether more remain.
func (m *Module) SyncFlow(ctx context.Context, tid string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	more := false
	service := db.AllProjects(ctx, "delivery flow projector: PAIMOS rounds, reviews and holds into project flow steps")
	_, err := m.flowWrite(service, tid, nil, func(ctx context.Context, tx pgx.Tx, apply func([]flowBatch) error) error {
		var after int64
		err := tx.QueryRow(ctx, `SELECT last_event_id FROM delivery_flow_cursors FOR NO KEY UPDATE`).Scan(&after)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,type,node_id::text,at,before,after,metadata FROM events
			WHERE id>$1 AND (type LIKE 'delivery.work\_queue.%' OR type LIKE 'delivery.review.%' OR type='delivery.state_changed')
			ORDER BY id LIMIT $2`, after, flowSyncBatch)
		if err != nil {
			return err
		}
		var batch []flowSourceEvent
		for rows.Next() {
			var e flowSourceEvent
			if err := rows.Scan(&e.ID, &e.Type, &e.NodeID, &e.At, &e.Before, &e.After, &e.Metadata); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		more = len(batch) == flowSyncBatch
		for _, e := range batch {
			// Applied one event at a time: a close reads what earlier events opened.
			b, err := flowFromPaimosTx(ctx, tx, tid, e)
			if err != nil {
				return err
			}
			if b != nil {
				if err := apply([]flowBatch{*b}); err != nil {
					return err
				}
			}
			after = e.ID
		}
		if len(batch) == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO delivery_flow_cursors(tenant_id,last_event_id,updated_at) VALUES($1,$2,$3)
			ON CONFLICT(tenant_id) DO UPDATE SET last_event_id=EXCLUDED.last_event_id,updated_at=EXCLUDED.updated_at`, tid, after, m.now())
		return err
	})
	return more, err
}

// flowRoundSnapshot reads the fields of work and review round snapshots.
type flowRoundSnapshot struct {
	Round *struct {
		ID         string  `json:"id"`
		Project    string  `json:"project_id"`
		Ticket     string  `json:"ticket_node_id"`
		Kind       string  `json:"kind"`
		Number     int     `json:"round_number"`
		State      string  `json:"state"`
		Claimant   *string `json:"claimant_id"`
		PR         *int64  `json:"pull_request"`
		GateRounds int     `json:"gate_fix_rounds"`
		Verdict    string  `json:"effective_verdict"`
	} `json:"round"`
}

type flowTicket struct {
	Project, Key, Title string
}

func flowTicketTx(ctx context.Context, tx pgx.Tx, ticket string) (*flowTicket, error) {
	var t flowTicket
	err := tx.QueryRow(ctx, `SELECT project_id::text,coalesce(key,''),title FROM nodes WHERE id=$1 AND project_id IS NOT NULL`, ticket).Scan(&t.Project, &t.Key, &t.Title)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Key == "" {
		t.Key = ticket[:8]
	}
	if len([]rune(t.Title)) > 300 {
		t.Title = string([]rune(t.Title)[:300])
	}
	return &t, nil
}

func flowAgentTx(ctx context.Context, tx pgx.Tx, id *string, label string) (FlowActor, error) {
	a := FlowActor{Type: "agent", PrincipalID: id, Label: label}
	if id == nil {
		return a, nil
	}
	var kind string
	err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1`, *id).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		a.PrincipalID = nil
		return a, nil
	}
	if kind == "person" {
		a.Type = "person"
	}
	return a, err
}

func flowStr(s string) *string { return &s }

// flowFromPaimosTx maps one event to a step write, or nil.
func flowFromPaimosTx(ctx context.Context, tx pgx.Tx, tid string, e flowSourceEvent) (*flowBatch, error) {
	if e.Type == "delivery.state_changed" {
		return flowFromHoldTx(ctx, tx, tid, e)
	}
	var snap flowRoundSnapshot
	if len(e.After) == 0 || json.Unmarshal(e.After, &snap) != nil || snap.Round == nil || snap.Round.ID == "" || snap.Round.Ticket == "" {
		return nil, nil
	}
	r := snap.Round
	t, err := flowTicketTx(ctx, tx, r.Ticket)
	if err != nil || t == nil {
		return nil, err
	}
	at := e.At
	b := &flowBatch{Item: flowItemInput{Project: t.Project, Kind: "change", Ref: t.Key, Title: t.Title, Ticket: &r.Ticket, NoCreate: true}}
	if r.PR != nil && *r.PR > 0 {
		b.Item.PRs = []int64{*r.PR}
		// The round names no repository; the delivery linking this ticket and
		// pull request does. A merge projected earlier still closes the change.
		if b.Item.Repository, err = flowLinkedRepositoryTx(ctx, tx, t.Project, b.Item.Ticket, b.Item.PRs); err != nil {
			return nil, err
		}
	}
	if e.Type == "delivery.review.queued" || e.Type == "delivery.review.claim" || e.Type == "delivery.review.verdict" {
		round := r.GateRounds + 1
		if round < 1 || round > 1000 {
			round = 1
		}
		key := "review/" + r.ID
		reviewer, err := flowAgentTx(ctx, tx, r.Claimant, "Reviewer")
		if err != nil {
			return nil, err
		}
		wait := flowStepInput{Source: "paimos", Key: key + "/wait", StepKey: "review", Round: round, Kind: "wait", Actor: FlowActor{Type: "agent", Label: "Reviewer"}, Started: at, WaitReason: flowStr("reviewer"), Merge: true}
		switch r.State {
		case "queued":
			b.Steps = []flowStepInput{wait}
		case "claimed":
			wait.Ended, wait.CloseOnly = &at, true
			b.Steps = []flowStepInput{wait, {Source: "paimos", Key: key, StepKey: "review", Round: round, Kind: "work", Actor: reviewer, Started: at, Merge: true}}
		case "completed":
			var outcome *string
			if r.Verdict == "ok" || r.Verdict == "changes" {
				outcome = &r.Verdict
			}
			wait.Ended, wait.CloseOnly = &at, true
			b.Steps = []flowStepInput{wait, {Source: "paimos", Key: key, StepKey: "review", Round: round, Kind: "work", Actor: reviewer, Started: at, Ended: &at, Outcome: outcome, CloseOnly: true}}
		default:
			return nil, nil
		}
		return b, nil
	}
	stepKey, kind, label := "build", "work", "Builder"
	switch r.Kind {
	case "first_build":
	case "fix":
		kind = "rework"
	case "merge":
		stepKey, kind = "merge_round", "rework"
	case "land":
		stepKey = "queue"
	default:
		return nil, nil
	}
	round := r.Number
	if round < 1 || round > 1000 {
		round = 1
	}
	key := "work/" + r.ID
	builder, err := flowAgentTx(ctx, tx, r.Claimant, label)
	if err != nil {
		return nil, err
	}
	work := flowStepInput{Source: "paimos", Key: key, StepKey: stepKey, Round: round, Kind: kind, Actor: builder, Started: at, Merge: true}
	// Each parking episode has its own key. Closing matches an earlier open
	// episode, never the row this event itself opens on replay.
	own := key + "/parked/" + strconv.FormatInt(e.ID, 10)
	hold := flowStepInput{Source: "paimos", Key: own, StepKey: "hold", Round: round, Kind: "wait", Actor: FlowActor{Type: "agent", Label: "LEAD"}, Started: at, WaitReason: flowStr("dependency"), Merge: true}
	closeHold, err := flowCloseParkTx(ctx, tx, tid, t.Project, t.Key, key, own, e.ID, at)
	if err != nil {
		return nil, err
	}
	steps := []flowStepInput{}
	if closeHold != nil {
		steps = append(steps, *closeHold)
	}
	switch r.State {
	case "claimed", "running":
		steps = append(steps, work)
	case "done":
		work.Ended, work.Outcome, work.CloseOnly = &at, flowStr("ok"), true
		steps = append(steps, work)
	case "parked":
		work.Ended, work.CloseOnly = &at, true
		steps = append(steps, work, hold)
	case "queued":
		if closeHold == nil {
			return nil, nil
		}
	default:
		return nil, nil
	}
	b.Steps = steps
	return b, nil
}

// flowCloseParkTx finds the open parking episode of this round opened by an
// earlier event. Causal order is the event id, not the timestamp: equal
// timestamps must not let a replay of an earlier event close a later episode.
func flowCloseParkTx(ctx context.Context, tx pgx.Tx, tid, project, ref, workKey, ownKey string, eventID int64, at time.Time) (*flowStepInput, error) {
	item := flowItemID(tid, project, "change", ref)
	prefix := workKey + "/parked/"
	var open string
	err := tx.QueryRow(ctx, `SELECT source_key FROM delivery_flow_steps WHERE item_id=$1 AND source='paimos' AND ended_at IS NULL AND left(source_key,length($2::text))=$2 AND source_key<>$3 AND CASE WHEN substring(source_key from length($2::text)+1) ~ '^[0-9]+$' THEN substring(source_key from length($2::text)+1)::bigint ELSE 0 END < $4 ORDER BY CASE WHEN substring(source_key from length($2::text)+1) ~ '^[0-9]+$' THEN substring(source_key from length($2::text)+1)::bigint ELSE 0 END DESC LIMIT 1`, item, prefix, ownKey, eventID).Scan(&open)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &flowStepInput{Source: "paimos", Key: open, StepKey: "hold", Round: 1, Kind: "wait", Started: at, Ended: &at, CloseOnly: true}, nil
}

// flowFromHoldTx records delivery holds as waits and a merge as the end of
// the change. Each hold episode is its own step, keyed by the event that
// started it.
func flowFromHoldTx(ctx context.Context, tx pgx.Tx, tid string, e flowSourceEvent) (*flowBatch, error) {
	if e.NodeID == nil {
		return nil, nil
	}
	var before, after struct {
		State string `json:"state"`
	}
	var meta struct {
		Item string `json:"delivery_item_id"`
		PR   *int64 `json:"pull_request"`
	}
	_ = json.Unmarshal(e.Before, &before)
	if json.Unmarshal(e.After, &after) != nil || json.Unmarshal(e.Metadata, &meta) != nil || meta.Item == "" {
		return nil, nil
	}
	held, wasHeld := after.State == string(Held), before.State == string(Held)
	merged := after.State == string(Merged) && before.State != string(Merged)
	if held == wasHeld && !merged {
		return nil, nil
	}
	t, err := flowTicketTx(ctx, tx, *e.NodeID)
	if err != nil || t == nil {
		return nil, err
	}
	at := e.At
	b := &flowBatch{Item: flowItemInput{Project: t.Project, Kind: "change", Ref: t.Key, Title: t.Title, Ticket: e.NodeID, NoCreate: true}}
	if meta.PR != nil && *meta.PR > 0 {
		b.Item.PRs = []int64{*meta.PR}
	}
	if merged {
		b.Item.Ended = &at
	}
	prefix := "hold/" + meta.Item + "/"
	if held && !wasHeld {
		b.Steps = append(b.Steps, flowStepInput{Source: "paimos", Key: prefix + strconv.FormatInt(e.ID, 10), StepKey: "hold", Round: 1, Kind: "wait", Actor: FlowActor{Type: "agent", Label: "LEAD"}, Started: at, WaitReason: flowStr("dependency"), Merge: true})
	}
	if wasHeld && !held {
		item := flowItemID(tid, t.Project, "change", t.Key)
		var open string
		err := tx.QueryRow(ctx, `SELECT source_key FROM delivery_flow_steps WHERE item_id=$1 AND source='paimos' AND step_key='hold' AND ended_at IS NULL AND left(source_key,length($2))=$2 ORDER BY started_at DESC LIMIT 1`, item, prefix).Scan(&open)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if open != "" {
			b.Steps = append(b.Steps, flowStepInput{Source: "paimos", Key: open, StepKey: "hold", Round: 1, Kind: "wait", Started: at, Ended: &at, CloseOnly: true})
		}
	}
	if !merged && len(b.Steps) == 0 {
		return nil, nil
	}
	return b, nil
}
