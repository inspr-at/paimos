// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"io"
	"net/http"
	"strings"
)

func sendHarnessMessage(w http.ResponseWriter, r *http.Request) {
	projectID, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	message, decodeErr := agentd.DecodeNativeMessage(raw)
	if err != nil || decodeErr != nil {
		jsonError(w, "invalid native message", http.StatusBadRequest)
		return
	}
	agent, _ := readAgentAttribution(r)
	leases := r.Header.Values(harnessWorkerLeaseHeader)
	keys := r.Header.Values("Idempotency-Key")
	if agent == nil || len(leases) != 1 || leases[0] != strings.TrimSpace(leases[0]) || len(keys) != 1 || len(keys[0]) < 1 || len(keys[0]) > 128 {
		jsonError(w, "owned sender proof and idempotency key required", http.StatusForbidden)
		return
	}
	authority := harnessRegistrationAuthority(r, projectID)
	out, err := agentmessage.NewService(db.DB).SendEnvelope(r.Context(), agentmessage.SendEnvelopeInput{
		To: message.To, Body: message.Body, ReplyTo: message.ReplyTo, ActionRequest: message.ActionRequest,
		ExpectsReply: message.ExpectsReply, DeliveryLevel: "simple", IdempotencyKey: keys[0],
		SenderAuthority: func(ctx context.Context, tx *sql.Tx) (agentmessage.SenderBinding, error) {
			if _, err := authority(ctx, tx); err != nil {
				return agentmessage.SenderBinding{}, err
			}
			return managedharness.MessageSenderTx(ctx, tx, projectID, chi.URLParam(r, "sessionID"), *agent, leases[0])
		},
	})
	if err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	jsonOK(w, agentd.NativeMessageReceipt{MessageID: out.MessageID, ThreadID: out.ThreadID, Delivered: out.Delivered, HeldReason: out.HeldReason})
}
