// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// A person's read marker for one session chat. Absent fields are null until
// the person has marked anything. Agents are refused before the row is read.
type sessionReadMarker struct {
	SessionID string     `json:"session_id"`
	MessageID *string    `json:"last_read_message_id"`
	EventID   *int64     `json:"last_read_event_id"`
	ReadAt    *time.Time `json:"read_at"`
}

type sessionReadMarkerWrite struct {
	MessageID string `json:"last_read_message_id"`
	EventID   *int64 `json:"last_read_event_id"`
}

func (m *Module) getReadMarker(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "person required")
	}
	sessionID := r.PathValue("sessionId")
	if _, err := load(r.Context(), tx, r.PathValue("projectId"), sessionID, false); err != nil {
		return nil, err
	}
	return scanReadMarker(r.Context(), tx, p.ID, sessionID)
}

func (m *Module) putReadMarker(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "person required")
	}
	projectID, sessionID := r.PathValue("projectId"), r.PathValue("sessionId")
	if _, err := load(r.Context(), tx, projectID, sessionID, false); err != nil {
		return nil, err
	}
	var in sessionReadMarkerWrite
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.MessageID) || in.EventID == nil || *in.EventID < 0 {
		return nil, workorders.Fail(400, "invalid read marker")
	}
	messageID, err := canonicalReadMessage(r.Context(), tx, projectID, sessionID, in.MessageID, *in.EventID)
	if err != nil {
		return nil, err
	}
	var storedMessage string
	var storedEvent int64
	var at time.Time
	err = tx.QueryRow(r.Context(), `
		INSERT INTO session_read_markers(tenant_id, person_id, session_id, last_read_message_id, last_read_event_id)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tenant_id, person_id, session_id) DO UPDATE
			SET last_read_message_id = EXCLUDED.last_read_message_id,
				last_read_event_id = EXCLUDED.last_read_event_id,
				read_at = clock_timestamp()
			WHERE session_read_markers.last_read_event_id < EXCLUDED.last_read_event_id
		RETURNING last_read_message_id::text, last_read_event_id, read_at`,
		p.TenantID, p.ID, sessionID, messageID, *in.EventID).Scan(&storedMessage, &storedEvent, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return scanReadMarker(r.Context(), tx, p.ID, sessionID)
	}
	if err != nil {
		return nil, err
	}
	return sessionReadMarker{SessionID: sessionID, MessageID: &storedMessage, EventID: &storedEvent, ReadAt: &at}, nil
}

// The claimed message must belong to the session. The event may be that
// message or a later one in the same session, so a collapsed group can mark
// its newest post while naming the group's first id. The stored id is the
// message that owns the event.
func canonicalReadMessage(ctx context.Context, tx pgx.Tx, projectID, sessionID, messageID string, eventID int64) (string, error) {
	var messageEvent int64
	err := tx.QueryRow(ctx, `
		SELECT sent_event_id FROM inbox_compat_messages
		WHERE project_id=$1 AND id=$2 AND (sender_session_id=$3 OR recipient_session_id=$3)`,
		projectID, messageID, sessionID).Scan(&messageEvent)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && eventID < messageEvent) {
		return "", workorders.Fail(400, "message is not in this session")
	}
	if err != nil {
		return "", err
	}
	if eventID == messageEvent {
		return messageID, nil
	}
	var owner string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM inbox_compat_messages
		WHERE project_id=$1 AND sent_event_id=$2 AND (sender_session_id=$3 OR recipient_session_id=$3)`,
		projectID, eventID, sessionID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", workorders.Fail(400, "message is not in this session")
	}
	return owner, err
}

func scanReadMarker(ctx context.Context, tx pgx.Tx, personID, sessionID string) (sessionReadMarker, error) {
	out := sessionReadMarker{SessionID: sessionID}
	var message string
	var event int64
	var at time.Time
	err := tx.QueryRow(ctx, `
		SELECT last_read_message_id::text, last_read_event_id, read_at
		FROM session_read_markers WHERE person_id=$1 AND session_id=$2`, personID, sessionID).Scan(&message, &event, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return sessionReadMarker{}, err
	}
	out.MessageID, out.EventID, out.ReadAt = &message, &event, &at
	return out, nil
}
