// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

type AttachStatus struct {
	RequestID  string     `json:"request_id"`
	OwnerID    string     `json:"owner_id"`
	State      string     `json:"state"`
	LeaseUntil *time.Time `json:"lease_until"`
}

func readAttachStatus(ctx context.Context, tx pgx.Tx, session string) (*AttachStatus, error) {
	var out AttachStatus
	err := tx.QueryRow(ctx, `SELECT a.id::text,a.owner_id::text,
 CASE WHEN c.state<>'connected' OR s.stopped_at IS NOT NULL OR s.archived_at IS NOT NULL THEN 'detached'
 WHEN a.state='active' AND a.lease_until<=clock_timestamp() THEN 'unreachable' ELSE a.state END,a.lease_until
 FROM harness_attach_requests a JOIN harness_sessions s ON s.tenant_id=a.tenant_id AND s.id=a.session_id
 JOIN agent_pairing_computers c ON c.tenant_id=a.tenant_id AND c.id=a.computer_id WHERE a.session_id=$1`, session).Scan(&out.RequestID, &out.OwnerID, &out.State, &out.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &out, err
}
