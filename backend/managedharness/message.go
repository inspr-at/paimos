// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"context"
	"database/sql"
	"errors"
	"github.com/inspr-at/paimos/backend/agentmessage"
)

// MessageSenderTx proves the current owned worker generation in the same
// transaction as the send. Neither attribution nor a public UUID is a lease.
func MessageSenderTx(ctx context.Context, tx *sql.Tx, projectID int64, sessionID, agent, lease string) (agentmessage.SenderBinding, error) {
	deny := &agentmessage.CodedError{Code: "agent_message_forbidden", Err: errors.New("active owned message sender required")}
	ok, err := verifyWorkerLeaseQuery(ctx, tx, projectID, sessionID, lease)
	if err != nil || !ok {
		return agentmessage.SenderBinding{}, deny
	}
	var binding agentmessage.SenderBinding
	err = tx.QueryRowContext(ctx, `SELECT project_id,agent_name,id,ticket_id FROM harness_sessions
		WHERE project_id=? AND id=? AND agent_name=? AND management_mode='managed'
		AND phase IN ('working','yielded') AND advertised_inbox=1
		AND julianday(heartbeat_at) > julianday('now','-2 minutes')`, projectID, sessionID, agent).
		Scan(&binding.ProjectID, &binding.Agent, &binding.SessionID, &binding.IssueID)
	if err != nil {
		return agentmessage.SenderBinding{}, deny
	}
	return binding, nil
}
