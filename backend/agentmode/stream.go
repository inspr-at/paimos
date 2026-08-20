// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmode

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/delivery"
)

const MaxReplayBatch = 512

var ErrReset = errors.New("agent-mode stream reset required")

// WakeHub carries no payload: it only shortens the time until consumers tail
// the durable log. A size-one channel coalesces bursts without ever blocking a
// writer after commit.
type WakeHub struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]chan struct{}
}

func NewWakeHub() *WakeHub { return &WakeHub{subscribers: map[uint64]chan struct{}{}} }

func (h *WakeHub) Subscribe() (<-chan struct{}, func()) {
	if h == nil {
		h = DefaultWakeHub()
	}
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	ch := make(chan struct{}, 1)
	h.subscribers[id] = ch
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers, id)
			h.mu.Unlock()
		})
	}
}

func (h *WakeHub) Notify(_ context.Context, _ delivery.ChangeHint) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

var defaultWakeHub = NewWakeHub()

func DefaultWakeHub() *WakeHub { return defaultWakeHub }

// NotifyChange is the delivery.CommitObserver installed on accepted writers.
// Effects dispatch invokes it only after a successful caller-owned commit.
func NotifyChange(ctx context.Context, hint delivery.ChangeHint) { defaultWakeHub.Notify(ctx, hint) }

type StreamHint struct {
	DeliveryID       string `json:"delivery_id"`
	DeliveryRevision int64  `json:"delivery_revision"`
	ChangeSequence   int64  `json:"change_sequence"`
}

type StreamBatch struct {
	Kind   string
	Cursor string
	Hints  []StreamHint
}

type StreamerOptions struct {
	Clock     delivery.Clock
	Cursor    *CursorCodec
	Freshness delivery.FreshnessPolicy
	Hub       *WakeHub
}

type Streamer struct {
	db             *sql.DB
	clock          delivery.Clock
	cursor         *CursorCodec
	reader         *Reader
	hub            *WakeHub
	afterSubscribe func()
}

func NewStreamer(database *sql.DB, options StreamerOptions) *Streamer {
	clock := options.Clock
	if clock == nil {
		clock = delivery.ClockFunc(time.Now)
	}
	cursor := options.Cursor
	if cursor == nil {
		cursor = NewCursorCodec(clock)
	}
	hub := options.Hub
	if hub == nil {
		hub = DefaultWakeHub()
	}
	return &Streamer{db: database, clock: clock, cursor: cursor, hub: hub,
		reader: NewReader(database, ReaderOptions{Clock: clock, Cursor: cursor, Freshness: options.Freshness})}
}

type StreamSession struct {
	streamer  *Streamer
	request   Request
	binding   CursorBinding
	expiresAt time.Time
	highWater int64
	wake      <-chan struct{}
	cancel    func()
}

// Open subscribes before reading the durable high-water, closing the classic
// commit-between-snapshot-and-subscribe race. An empty resume token starts at
// deployment/current high-water; no pre-release history is guessed.
func (s *Streamer) Open(ctx context.Context, request Request, resumeToken string) (*StreamSession, error) {
	return s.OpenCompatible(ctx, []Request{request}, resumeToken)
}

// OpenCompatible accepts the canonical global-filter request first and, when
// project_id is present on the sole events route, the equivalent project-route
// request second. A resume token itself selects exactly one sealed scope before
// one authorization/catalog read; a fresh connection always uses the first.
func (s *Streamer) OpenCompatible(ctx context.Context, requests []Request, resumeToken string) (*StreamSession, error) {
	if s == nil || s.db == nil {
		return nil, ErrInvalid
	}
	if len(requests) == 0 || len(requests) > 2 || requests[0].UserID <= 0 {
		return nil, ErrInvalid
	}
	wake, cancel := s.hub.Subscribe()
	if s.afterSubscribe != nil {
		s.afterSubscribe()
	}
	request := requests[0]
	var decoded *CursorClaims
	if resumeToken != "" {
		scopes := make([]CursorBinding, len(requests))
		for index := range requests {
			if requests[index].UserID != requests[0].UserID {
				cancel()
				return nil, ErrReset
			}
			route, filter, err := requestFingerprints(requests[index])
			if err != nil {
				cancel()
				return nil, ErrReset
			}
			scopes[index].RouteDigest, scopes[index].FilterDigest = route, filter
		}
		claims, matched, err := s.cursor.DecodeScopes(resumeToken, requests[0].UserID, scopes)
		if err != nil {
			cancel()
			return nil, ErrReset
		}
		request = requests[matched]
		decoded = &claims
	}
	state, err := s.reader.StreamState(ctx, request)
	if err != nil {
		cancel()
		if resumeToken != "" && (errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrInvalid)) {
			return nil, ErrReset
		}
		return nil, err
	}
	after, expires := state.HighWater, s.clock.Now().UTC().Add(defaultCursorTTL)
	if decoded != nil {
		if !sameCursorBinding(decoded.CursorBinding, state.Binding) || decoded.HighWater < state.RetentionFloor ||
			decoded.HighWater > state.HighWater {
			cancel()
			return nil, ErrReset
		}
		after, expires = decoded.HighWater, decoded.ExpiresAt
	}
	return &StreamSession{streamer: s, request: request, binding: state.Binding, expiresAt: expires,
		highWater: after, wake: wake, cancel: cancel}, nil
}

func (s *StreamSession) Close() {
	if s != nil && s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

func (s *StreamSession) Wake() <-chan struct{} { return s.wake }

type replayRow struct {
	ID               int64
	DeliveryID       int64
	DeliveryKey      string
	DeliveryRevision int64
	ChangeSequence   int64
	PriorSequence    sql.NullInt64
	CurrentAudience  bool
	RevokedAudience  bool
	GoneAudience     bool
}

func (s *StreamSession) Drain(ctx context.Context) (StreamBatch, error) {
	if s == nil || s.streamer == nil || !s.expiresAt.After(s.streamer.clock.Now().UTC()) {
		return StreamBatch{}, ErrReset
	}
	state, err := s.streamer.reader.StreamState(ctx, s.request)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrInvalid) ||
			errors.Is(err, ErrInvariant) {
			return StreamBatch{}, ErrReset
		}
		return StreamBatch{}, err
	}
	if !sameCursorBinding(state.Binding, s.binding) || s.highWater < state.RetentionFloor || s.highWater > state.HighWater {
		return StreamBatch{}, ErrReset
	}
	rows, err := s.loadReplayRows(ctx, state.HighWater)
	if err != nil {
		return StreamBatch{}, err
	}
	if len(rows) == 0 {
		if state.HighWater > s.highWater {
			return StreamBatch{}, ErrReset
		}
		return StreamBatch{}, nil
	}
	if len(rows) > MaxReplayBatch {
		return StreamBatch{}, ErrReset
	}
	expected := s.highWater + 1
	for _, row := range rows {
		if row.ID != expected {
			return StreamBatch{}, ErrReset
		}
		expected++
	}
	// Reconstruct each delivery's sequence at the resume high-water from the
	// durable log. Hidden interleaved deliveries do not participate; for a
	// retained visible delivery, a missing per-delivery fact is a reset even
	// when the global log IDs happen to remain contiguous.
	sequenceByDelivery := map[int64]int64{}
	sequenceKnown := map[int64]bool{}
	for _, row := range rows {
		if !row.CurrentAudience && !row.RevokedAudience && !row.GoneAudience {
			continue
		}
		if !sequenceKnown[row.DeliveryID] {
			switch {
			case row.PriorSequence.Valid:
				sequenceByDelivery[row.DeliveryID] = row.PriorSequence.Int64
				sequenceKnown[row.DeliveryID] = true
			case state.RetentionFloor == 0:
				sequenceKnown[row.DeliveryID] = true
			default:
				// The authorized baseline itself was pruned. The retention
				// boundary is the only legitimate reason it can be absent.
				sequenceByDelivery[row.DeliveryID] = row.ChangeSequence
				sequenceKnown[row.DeliveryID] = true
				continue
			}
		}
		if row.ChangeSequence != sequenceByDelivery[row.DeliveryID]+1 {
			return StreamBatch{}, ErrReset
		}
		sequenceByDelivery[row.DeliveryID] = row.ChangeSequence
	}

	needReset, needRefetch := false, false
	candidates := map[string]replayRow{}
	for _, row := range rows {
		if row.RevokedAudience || row.GoneAudience {
			needReset = true
		}
		if row.CurrentAudience {
			needRefetch = true
			candidates[row.DeliveryKey] = row
		}
	}
	if needReset {
		return StreamBatch{}, ErrReset
	}

	hints := []StreamHint{}
	if needRefetch {
		fresh, err := s.streamer.reader.Read(ctx, s.request)
		if err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrInvalid) {
				return StreamBatch{}, ErrReset
			}
			return StreamBatch{}, err
		}
		visible := make(map[string]bool, len(fresh.Rows))
		for _, row := range fresh.Rows {
			visible[row.DeliveryID] = true
		}
		for _, row := range rows {
			candidate, ok := candidates[row.DeliveryKey]
			if !ok || !visible[row.DeliveryKey] {
				continue
			}
			// Coalesce repeated rows for one delivery to the last durable fact.
			if candidate.ID != row.ID {
				continue
			}
			hints = append(hints, StreamHint{DeliveryID: row.DeliveryKey,
				DeliveryRevision: row.DeliveryRevision, ChangeSequence: row.ChangeSequence})
		}
	}
	s.highWater = rows[len(rows)-1].ID
	cursor, err := s.streamer.cursor.Encode(s.binding, s.highWater)
	if err != nil {
		return StreamBatch{}, err
	}
	kind := "checkpoint"
	if needRefetch {
		kind = "refetch"
	}
	return StreamBatch{Kind: kind, Cursor: cursor, Hints: hints}, nil
}

func (s *StreamSession) loadReplayRows(ctx context.Context, tail int64) ([]replayRow, error) {
	scopeProject := int64(0)
	if s.request.RouteProjectID != nil {
		scopeProject = *s.request.RouteProjectID
	}
	query := auth.AgentModeAuthorizationCTE + `,
replay AS (
 SELECT change.id,change.delivery_id,change.delivery_key,change.delivery_revision,change.change_sequence,
	  (SELECT prior.change_sequence FROM delivery_change_log prior
	   WHERE prior.delivery_id=change.delivery_id AND prior.id<=?
	   ORDER BY prior.id DESC LIMIT 1) AS prior_sequence,
	  CASE WHEN live.issue_id IS NOT NULL
	    AND (?=0 OR live.project_id=?)
	    AND EXISTS(SELECT 1 FROM agent_mode_projects allowed
	     WHERE allowed.project_id=live.project_id)
	   THEN 1 ELSE 0 END AS current_audience,
	  CASE WHEN change.revoked_project_id IS NOT NULL
	    AND (?=0 OR change.revoked_project_id=?)
	    AND (?<>0 OR live.issue_id IS NULL)
	    AND EXISTS(SELECT 1 FROM agent_mode_projects allowed WHERE allowed.project_id=change.revoked_project_id)
   THEN 1 ELSE 0 END AS revoked_audience,
	  CASE WHEN live.issue_id IS NULL AND change.project_id_hint IS NOT NULL AND change.revoked_project_id IS NULL
    AND (?=0 OR change.project_id_hint=?)
    AND EXISTS(SELECT 1 FROM agent_mode_projects allowed WHERE allowed.project_id=change.project_id_hint)
   THEN 1 ELSE 0 END AS gone_audience
 FROM delivery_change_log change
 LEFT JOIN deliveries current_delivery ON current_delivery.id=change.delivery_id
 LEFT JOIN agent_mode_legacy_roots legacy ON legacy.synthetic_delivery_id=change.delivery_id
	 AND legacy.issue_id=change.root_issue_id AND legacy.delivery_key=change.delivery_key
 LEFT JOIN agent_mode_roots live ON live.issue_id=change.root_issue_id
	 AND (current_delivery.issue_id=live.issue_id OR (legacy.issue_id=live.issue_id
	  AND NOT EXISTS(SELECT 1 FROM deliveries canonical WHERE canonical.issue_id=legacy.issue_id)
	  AND EXISTS(SELECT 1 FROM agent_runs active_v0 WHERE active_v0.issue_id=legacy.issue_id
	   AND active_v0.delivery_instrumentation_version=0 AND active_v0.status IN ('queued','running'))))
 WHERE change.id>? AND change.id<=? ORDER BY change.id LIMIT 513
)
SELECT id,delivery_id,delivery_key,delivery_revision,change_sequence,prior_sequence,
 current_audience,revoked_audience,gone_audience FROM replay ORDER BY id`
	rows, err := s.streamer.db.QueryContext(ctx, query, s.request.UserID, s.highWater, scopeProject, scopeProject,
		scopeProject, scopeProject, scopeProject, scopeProject, scopeProject, s.highWater, tail)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []replayRow{}
	for rows.Next() {
		var row replayRow
		var current, revoked, gone int
		if err := rows.Scan(&row.ID, &row.DeliveryID, &row.DeliveryKey, &row.DeliveryRevision, &row.ChangeSequence,
			&row.PriorSequence,
			&current, &revoked, &gone); err != nil {
			return nil, err
		}
		row.CurrentAudience, row.RevokedAudience, row.GoneAudience = current == 1, revoked == 1, gone == 1
		out = append(out, row)
	}
	return out, rows.Err()
}

func sameCursorBinding(left, right CursorBinding) bool {
	return left.UserID == right.UserID && left.PermissionsEpoch == right.PermissionsEpoch &&
		subtle.ConstantTimeCompare(left.PermissionDigest[:], right.PermissionDigest[:]) == 1 &&
		subtle.ConstantTimeCompare(left.RouteDigest[:], right.RouteDigest[:]) == 1 &&
		subtle.ConstantTimeCompare(left.FilterDigest[:], right.FilterDigest[:]) == 1
}

func (s *StreamSession) String() string {
	if s == nil {
		return "agent-mode-stream<nil>"
	}
	return fmt.Sprintf("agent-mode-stream<high-water:%d>", s.highWater)
}
