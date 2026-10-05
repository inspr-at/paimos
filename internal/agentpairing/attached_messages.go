// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// SetAttachedMessages shares the inbox-owned payload service and service epoch.
// Nil or disabled keeps all new authority off, without changing watching.
func (m *Module) SetAttachedMessages(s *attachedmsg.Service) { m.messages = s }
func (m *Module) mountAttachedMessages(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agent-pairing/attach/{requestId}/messages", m.person("account.manage", m.messageCapability))
	mux.HandleFunc("POST /api/agent-pairing/attach/{requestId}/messages/approve", m.person("account.manage", m.messageApprove))
	mux.HandleFunc("POST /api/agent-pairing/attach/{requestId}/messages/revoke", m.person("account.manage", m.messageRevoke))
}
func messageError(w http.ResponseWriter, e error) {
	if !attachedmsg.WriteError(w, e) {
		WriteError(w, e)
	}
}
func (m *Module) messageCapability(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	if !uuidRE.MatchString(r.PathValue("requestId")) {
		messageError(w, attachedmsg.Fail(404, "not_found"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var out attachedmsg.Capability
	e := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var e error
		out, e = m.messages.Project(r.Context(), tx, p, r.PathValue("requestId"))
		return e
	})
	if e != nil {
		messageError(w, e)
		return
	}
	reply(w, out)
}
func (m *Module) messageApprove(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in struct {
		Generation string `json:"message_generation"`
		Digest     string `json:"consent_digest"`
	}
	if !uuidRE.MatchString(r.PathValue("requestId")) || decode(w, r, &in) != nil || !uuidRE.MatchString(in.Generation) || !hashRE.MatchString(in.Digest) {
		messageError(w, attachedmsg.Fail(400, "invalid_message_consent"))
		return
	}
	ctx, cancel := context.WithTimeout(attachedmsg.BrowserContext(r, m.origin, p), 10*time.Second)
	defer cancel()
	var out attachedmsg.Capability
	e := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if _, e := m.messages.Approve(ctx, tx, p, r.PathValue("requestId"), in.Generation, in.Digest); e != nil {
			return e
		}
		var e error
		out, e = m.messages.Project(ctx, tx, p, r.PathValue("requestId"))
		return e
	})
	if e != nil {
		messageError(w, e)
		return
	}
	reply(w, out)
}
func (m *Module) messageRevoke(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	if !uuidRE.MatchString(r.PathValue("requestId")) {
		messageError(w, attachedmsg.Fail(404, "not_found"))
		return
	}
	ctx, cancel := context.WithTimeout(attachedmsg.BrowserContext(r, m.origin, p), 10*time.Second)
	defer cancel()
	var grants []string
	e := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		var e error
		grants, e = attachedmsg.Revoke(ctx, tx, p, r.PathValue("requestId"))
		return e
	})
	if e != nil {
		messageError(w, e)
		return
	}
	for _, g := range grants {
		m.messages.PurgeGrant(p.TenantID, g)
	}
	reply(w, map[string]string{"state": "messages_off"})
}

// Called only after checking the registered memory-only key and protocol 2.
// No message operation reaches the watch poll's lease/heartbeat update.
func (m *Module) messageDevice(w http.ResponseWriter, r *http.Request, p tenant.Principal, in attachwatch.DeviceRequest, registered watchPollKey) {
	if !registered.messages || in.MessageProtocol != attachedmsg.Protocol || !m.messages.Enabled() {
		messageError(w, attachedmsg.Fail(409, "attached_messages_unavailable"))
		return
	}
	if in.Text != "" || in.Doing != nil || in.ToolActivity != nil {
		messageError(w, attachedmsg.Fail(400, "message_operation_rejects_watch_text"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if in.Operation == "message_offer" || (in.Operation == "message_receipt" || in.Operation == "message_validate") {
		m.messageDelivery(w, r, p, in, registered)
		return
	}
	var out attachedmsg.Capability
	e := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		a, e := attachedmsg.LoadAttachment(r.Context(), tx, in.RequestID)
		if e != nil {
			return e
		}
		if a.Principal != p.ID || a.Computer != in.ComputerID || a.Digest != in.Digest || a.Digest != in.Snapshot.Digest() {
			return attachedmsg.Fail(403, "message_binding_changed")
		}
		var g attachedmsg.Grant
		switch in.Operation {
		case "message_request":
			if in.MessageLocalAuthNonce != "" || in.MessageLocalAuthSignature != "" || in.MessageConsentDigest != "" || in.MessageGeneration != "" {
				return attachedmsg.Fail(400, "invalid_message_request")
			}
			g, e = m.messages.Prepare(r.Context(), tx, in.RequestID, attachedmsg.Prepare{ReleaseDigest: in.HookReleaseDigest, ConfigDigest: in.HookConfigDigest, Version: in.QualifiedHarnessVersion, DaemonEpoch: registered.hash})
		case "message_activate", "message_observed":
			g, e = m.messages.Activate(r.Context(), tx, in.RequestID, in.MessageGeneration, in.MessageConsentDigest, registered.hash, in.HookReleaseDigest, in.HookConfigDigest, in.QualifiedHarnessVersion, in.MessageLocalAuthNonce, in.MessageLocalAuthSignature, in.Operation == "message_observed")
		default:
			return attachedmsg.Fail(400, "invalid_message_operation")
		}
		if e != nil {
			return e
		}
		out.Grant = &g
		out.Ready = g.State == "active" && g.ObservedAt != nil
		if !out.Ready {
			switch g.State {
			case "pending":
				out.Blocker = "owner_consent_required"
			case "approved":
				out.Blocker = "local_activation_required"
			default:
				out.Blocker = "waiting_for_hook"
			}
		}
		return nil
	})
	if e != nil {
		messageError(w, e)
		return
	}
	reply(w, out)
}

// The watch-key read lock spans both commits. Registration/restart cannot
// replace the daemon epoch while bytes are being released.
func (m *Module) messageDelivery(w http.ResponseWriter, r *http.Request, p tenant.Principal, in attachwatch.DeviceRequest, registered watchPollKey) {
	var b attachedmsg.Binding
	if json.Unmarshal(in.MessageBinding, &b) != nil || b.TenantID != p.TenantID || b.AttachRequestID != in.RequestID || b.ComputerID != in.ComputerID || b.DaemonEpoch != registered.hash || b.Generation != in.MessageGeneration || b.Digest() != in.MessageConsentDigest || b.SnapshotDigest != in.Digest || in.Snapshot.Digest() != in.Digest || b.HookReleaseDigest != in.HookReleaseDigest || b.HookConfigDigest != in.HookConfigDigest || b.HarnessVersion != in.QualifiedHarnessVersion || in.AttachProtocol != attachwatch.Protocol {
		messageError(w, attachedmsg.Fail(403, "message_binding_changed"))
		return
	}
	var receipt attachedmsg.Receipt
	if (in.Operation == "message_receipt" || in.Operation == "message_validate") && (json.Unmarshal(in.MessageReceipt, &receipt) != nil || !uuidRE.MatchString(receipt.MessageID) || !uuidRE.MatchString(receipt.DeliveryID) || !hashRE.MatchString(receipt.Nonce)) {
		messageError(w, attachedmsg.Fail(400, "invalid_message_receipt"))
		return
	}
	if in.Operation == "message_offer" && len(in.MessageReceipt) > 0 {
		messageError(w, attachedmsg.Fail(400, "invalid_message_offer"))
		return
	}
	var offer *attachedmsg.Offer
	out := attachedmsg.Exchange{State: "empty"}
	e := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		// Unlike live validation, this association also works for a revoked/offline
		// attachment so a genuine late receipt can terminalize an in-flight offer.
		var principal string
		if e := tx.QueryRow(r.Context(), `SELECT c.principal_id::text FROM harness_attach_requests a JOIN agent_pairing_computers c ON c.tenant_id=a.tenant_id AND c.id=a.computer_id WHERE a.id=$1::uuid AND a.computer_id=$2::uuid`, in.RequestID, in.ComputerID).Scan(&principal); e != nil || principal != p.ID {
			return attachedmsg.Fail(403, "message_binding_changed")
		}
		var err error
		if in.Operation == "message_offer" {
			offer, err = m.messages.Claim(r.Context(), tx, b, in.MessageEpoch)
		} else if in.Operation == "message_validate" {
			out, err = m.messages.ValidateDisclosure(r.Context(), tx, b, receipt)
		} else {
			out, err = m.messages.Settle(r.Context(), tx, b, receipt)
		}
		return err
	})
	if e == nil && offer != nil {
		e = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
			var err error
			out, err = m.messages.Release(r.Context(), tx, offer)
			return err
		})
	}
	if e != nil {
		messageError(w, e)
		return
	}
	if out.Offer != nil && (!time.Now().Before(out.Offer.Deadline) || r.Context().Err() != nil) {
		// Commit/response latency can consume the remaining handoff budget. Never
		// put expired bytes on the wire; the committed offer sweeps to uncertain.
		out = attachedmsg.Exchange{State: "uncertain"}
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, out)
}
