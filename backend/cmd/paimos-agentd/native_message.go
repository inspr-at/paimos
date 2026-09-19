// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"strconv"
	"time"
)

func (r *cliReporter) SendNativeMessage(ctx context.Context, session agentd.Session, callID string, message agentd.NativeMessage) agentd.NativeMessageReceipt {
	failed := agentd.NativeMessageReceipt{Error: "sender_unavailable"}
	if !safeReporterValue(callID, 256) || session.State != agentd.StateRunning || !session.Managed {
		return failed
	}
	// Serialize with registration/closure: a child cannot choose another
	// session's proof, and terminal cleanup cannot recreate its lease here.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !r.mu.TryLock() {
		select {
		case <-ctx.Done():
			return failed
		case <-ticker.C:
		}
	}
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return failed
	}
	known, ok := r.sessions[session.ID]
	if !ok || known.terminal || known.identity != session.Identity || known.projectID != session.ProjectID || uuid.Validate(known.publicID) != nil {
		return failed
	}
	_, agent, err := reporterIdentity(session)
	if err != nil {
		return failed
	}
	lease, err := r.leases.GetOrCreate(session.ID)
	if err != nil {
		return failed
	}
	frame, err := json.Marshal(map[string]any{"worker_lease": lease, "message": message})
	if err != nil {
		return failed
	}
	key := uuid.NewSHA1(uuid.NameSpaceOID, []byte(session.ID+"/"+callID)).String()
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{"--json", "message", "send-native", "--project-id", strconv.FormatInt(session.ProjectID, 10), "--session", known.publicID, "--agent", agent, "--idempotency-key", key}
	raw, err := r.run(callCtx, r.paimosPath, args, r.environment, bytes.NewReader(frame))
	if err != nil {
		return agentd.NativeMessageReceipt{Error: "send_failed"}
	}
	var receipt agentd.NativeMessageReceipt
	if json.Unmarshal(raw, &receipt) != nil || uuid.Validate(receipt.MessageID) != nil || uuid.Validate(receipt.ThreadID) != nil {
		return agentd.NativeMessageReceipt{Error: "send_failed"}
	}
	// Never forward arbitrary errors or response fields into the native model.
	receipt.Error = ""
	switch receipt.HeldReason {
	case "", "action request - requires human approval", "sender not in receiver allowlist":
	default:
		receipt.HeldReason = "held"
	}
	return receipt
}
