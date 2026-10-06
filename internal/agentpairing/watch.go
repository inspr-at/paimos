// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentactivity"
	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Poll authority is intentionally absent from pairing.json and every database
// row. Losing either process's memory requires registration and fresh consent.
// Serialize registration against exchanges through commit and relay publication.
type watchPollKeys struct {
	sync.RWMutex
	keys map[string]watchPollKey
}
type watchPollKey struct {
	hash         string
	protocol     int
	proofVersion int
	messages     bool
}

func (m *Module) registerWatchKey(ctx context.Context, p tenant.Principal, in attachwatch.DeviceRequest) error {
	protocol := in.AttachProtocol
	if protocol == 0 {
		protocol = 1 // Legacy daemons must keep serving, with attach disabled.
	}
	if !hashRE.MatchString(in.PollKey) || !hashRE.MatchString(in.DeviceProof) || in.PollKey == in.DeviceProof || in.Text != "" {
		return fail(403, "forbidden", "fresh daemon poll key required")
	}
	capability := attachwatch.LocalAuthUnreported
	if in.LocalAuthCapability != "" {
		if !attachwatch.LocalAuthCapabilityReported(in.LocalAuthCapability) {
			return fail(400, "invalid_request", "invalid local auth capability")
		}
		capability = in.LocalAuthCapability
	}
	m.watchKeys.Lock()
	defer m.watchKeys.Unlock()
	key := p.TenantID + "/" + in.ComputerID
	if _, exists := m.watchKeys.keys[key]; !exists && len(m.watchKeys.keys) >= 4096 {
		return fail(429, "rate_limited", "daemon registration capacity reached")
	}
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{}) != nil {
			return fail(403, "forbidden", "paired daemon required")
		}
		_, principal, _, _, proof, err := attachComputer(ctx, tx, in.ComputerID)
		if err != nil || principal != p.ID || subtle.ConstantTimeCompare([]byte(proof), []byte(digest(in.DeviceProof))) != 1 {
			return fail(403, "forbidden", "computer proof rejected")
		}
		if protocol != 1 && protocol != attachwatch.Protocol {
			return attachFailure(409, "update_agentd", "update agentd to attach protocol 2; fresh approval required", attachwatch.RefusalVersion)
		}
		if protocol == attachwatch.Protocol && in.LocalConsentProofVersion != attachwatch.LocalConsentProofVersion {
			return attachFailure(409, "update_agentd", "upgrade paimos-agentd to local consent proof v2 and restart; existing pairing keys remain valid; fresh approval required", attachwatch.RefusalVersion)
		}
		// Even a caller holding the lifecycle proof cannot take over a previous
		// approval by registering another key. No pending/approved watch survives.
		if _, err = tx.Exec(ctx, `WITH ended AS (
 UPDATE harness_attach_requests SET state='detached',lease_until=NULL,local_auth_nonce=NULL
 WHERE computer_id=$1 AND state IN ('pending','approved','active') RETURNING session_id)
 UPDATE harness_sessions SET phase='stopped',stopped_at=coalesce(stopped_at,clock_timestamp()),
 stop_reason='watch daemon restarted; process exit unconfirmed' WHERE id IN (SELECT session_id FROM ended)`, in.ComputerID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE agent_pairing_computers SET local_auth_capability=$2 WHERE id=$1`, in.ComputerID, capability)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(403, "forbidden", "computer proof rejected")
		}
		return nil
	})
	if err == nil {
		if m.watchKeys.keys == nil {
			m.watchKeys.keys = make(map[string]watchPollKey)
		}
		m.watchKeys.keys[key] = watchPollKey{hash: digest(in.PollKey), protocol: protocol, proofVersion: in.LocalConsentProofVersion, messages: in.MessageProtocol == attachedmsg.Protocol}
	}
	return err
}

func (m *Module) mountWatch(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me/security/session-watching", m.person("profile.read", m.watchSecurity))
	mux.HandleFunc("PUT /api/me/security/session-watching", m.person("profile.write", m.watchSecurity))
	mux.HandleFunc("POST /api/agent-pairing/attach", m.attachDevice)
	mux.HandleFunc("POST /api/agent-pairing/attach/lookup", m.person("account.manage", m.attachLookup))
	mux.HandleFunc("GET /api/agent-pairing/attach/pending", m.person("account.manage", m.attachPending))
	mux.HandleFunc("POST /api/agent-pairing/attach/{requestId}/approve", m.person("account.manage", m.attachApprove))
	mux.HandleFunc("POST /api/agent-pairing/attach/{requestId}/revoke", m.person("account.manage", m.attachRevoke))
	mux.HandleFunc("GET /api/projects/{projectId}/harness-sessions/{sessionId}/watch", m.attachStream)
}
func (m *Module) attachLimit(ctx context.Context, tenantID, bucket string, max int) error {
	var n int
	err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO harness_attach_limits(tenant_id,bucket,attempts) VALUES($1,$2,1)
 ON CONFLICT(tenant_id,bucket) DO UPDATE SET
 attempts=CASE WHEN harness_attach_limits.starts_at<clock_timestamp()-($3::integer * interval '1 second') THEN 1 ELSE harness_attach_limits.attempts+1 END,
 starts_at=CASE WHEN harness_attach_limits.starts_at<clock_timestamp()-($3::integer * interval '1 second') THEN clock_timestamp() ELSE harness_attach_limits.starts_at END RETURNING attempts`, tenantID, bucket, int(attachwatch.AttemptWindow/time.Second)).Scan(&n)
	})
	if err != nil {
		return err
	}
	if n > max {
		return fail(429, "rate_limited", "attach attempt cap reached")
	}
	return nil
}
func loadAttach(ctx context.Context, tx pgx.Tx, id string) (attachwatch.View, string, string, error) {
	var v attachwatch.View
	var owner, code string
	if !uuidRE.MatchString(id) {
		return v, owner, code, fail(404, "not_found", "attach unavailable")
	}
	err := tx.QueryRow(ctx, `SELECT id::text,digest,snapshot,state,expires_at,lease_until,session_id::text,owner_id::text,user_code,consent_mode,coalesce(local_auth_nonce,'') FROM harness_attach_requests WHERE id=$1 FOR UPDATE`, id).Scan(&v.RequestID, &v.Digest, &v.Snapshot, &v.State, &v.ExpiresAt, &v.LeaseUntil, &v.SessionID, &owner, &code, &v.ConsentMode, &v.LocalAuthNonce)
	if err == nil && v.State == "pending" {
		v.ConsentMode, err = computerWatchConsentMode(ctx, tx, owner, v.Snapshot.Platform, v.Snapshot.ComputerID)
	}
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT coalesce((SELECT agent_activity_mode FROM harness_settings),'agent_summary')`).Scan(&v.AgentActivityMode)
	}
	v.ConsentDigest = attachwatch.ConsentDigest(v.RequestID, v.Digest, v.ConsentMode)
	v.LocalConsentProofVersion = attachwatch.LocalConsentProofVersion
	return v, owner, code, err
}

// The original pairing approver owns the computer. The approved workspace is
// the explicit tenant/computer cwd allowlist; a request cannot expand it.
func attachComputer(ctx context.Context, tx pgx.Tx, computer string) (owner, principal, host, workspace, proof string, err error) {
	err = tx.QueryRow(ctx, `SELECT q.approved_by::text,c.principal_id::text,q.details->>'computer_name',q.details->>'workspace_path',c.lifecycle_hash
 FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 WHERE c.id=$1 AND c.state='connected' AND q.state='redeemed'`, computer).Scan(&owner, &principal, &host, &workspace, &proof)
	return
}
func attachScope(ctx context.Context, tx pgx.Tx, tenantID, owner string, s attachwatch.Snapshot) error {
	p := tenant.Principal{ID: owner, TenantID: tenantID, Kind: tenant.Person}
	if authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}) != nil {
		return fail(403, "forbidden", "owner delegation no longer valid")
	}
	if authz.RequireTx(ctx, tx, p, "harness.write", authz.Scope{ProjectID: s.ProjectID}) != nil {
		return attachFailure(403, "forbidden", "owner delegation no longer valid", attachwatch.RefusalTicket)
	}
	var ticketOK, enrollmentOK, draining bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes p JOIN node_kinds k ON k.tenant_id=p.tenant_id AND k.id=p.kind_id JOIN nodes t ON t.tenant_id=p.tenant_id AND t.project_id=p.id WHERE p.id=$1 AND k.slug='project' AND p.deleted_at IS NULL AND t.id=$2 AND t.deleted_at IS NULL), EXISTS(SELECT 1 FROM agent_pairing_enrollments e JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id WHERE e.computer_id=$3 AND e.state='connected' AND a.harness=$4), EXISTS(SELECT 1 FROM agent_pairing_enrollments e JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id WHERE e.computer_id=$3 AND e.state='draining' AND a.harness=$4)`, s.ProjectID, s.TicketID, s.ComputerID, s.Harness).Scan(&ticketOK, &enrollmentOK, &draining)
	if err != nil {
		return err
	}
	if !ticketOK {
		return attachFailure(409, "conflict", "project, ticket or harness enrollment changed", attachwatch.RefusalTicket)
	}
	if !enrollmentOK {
		// An existing OS process does not carry attach authority. Drain preserves
		// owned work for settlement, but cannot grant a new session/consent lease.
		cause := attachwatch.RefusalEnrollment
		if draining {
			cause = attachwatch.RefusalDraining
		}
		return attachFailure(409, "conflict", "project, ticket or harness enrollment changed", cause)
	}
	return nil
}
func attachEnd(ctx context.Context, tx pgx.Tx, v *attachwatch.View, state string) error {
	_, err := tx.Exec(ctx, `UPDATE harness_attach_requests SET state=$2,lease_until=NULL,local_auth_nonce=NULL WHERE id=$1`, v.RequestID, state)
	if err == nil && v.SessionID != nil {
		reason := "attach " + state + "; process exit unconfirmed"
		if state == "confirmed_exited" {
			reason = "attach confirmed exited by kernel process check"
		}
		_, err = tx.Exec(ctx, `UPDATE harness_sessions SET phase='stopped',stopped_at=coalesce(stopped_at,clock_timestamp()),stop_reason=$2 WHERE id=$1`, *v.SessionID, reason)
	}
	v.State = state
	v.LeaseUntil = nil
	v.LocalAuthNonce = ""
	return err
}
func attachExpired(ctx context.Context, tx pgx.Tx, v *attachwatch.View) (bool, error) {
	var expired bool
	err := tx.QueryRow(ctx, `SELECT CASE WHEN state='active' THEN lease_until<=clock_timestamp() WHEN state IN ('pending','approved') THEN expires_at<=clock_timestamp() ELSE true END FROM harness_attach_requests WHERE id=$1`, v.RequestID).Scan(&expired)
	if err != nil {
		return false, err
	}
	if expired && v.State != "detached" && v.State != "unreachable" && v.State != "confirmed_exited" {
		err = attachEnd(ctx, tx, v, "unreachable")
	}
	return expired, err
}
func (m *Module) attachDevice(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Agent || authz.Require(authz.BindPool(r.Context(), m.pool), "harness.worker", authz.Scope{}) != nil {
		WriteError(w, fail(403, "forbidden", "paired daemon required"))
		return
	}
	var in attachwatch.DeviceRequest
	// JSON escapes can expand a bounded text record sixfold.
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	if workorders.Decode(r, &in) != nil || !uuidRE.MatchString(in.ComputerID) || len(in.Text) > attachwatch.MaxText || !inertText(in.Text) {
		WriteError(w, fail(400, "invalid_request", "invalid attach request"))
		return
	}
	// Explicit "unreported" is not a daemon report. Empty is stored only on register.
	// A later poll cannot change the stored report.
	if in.LocalAuthCapability != "" && !attachwatch.LocalAuthCapabilityReported(in.LocalAuthCapability) {
		WriteError(w, fail(400, "invalid_request", "invalid local auth capability"))
		return
	}
	if in.LocalConfirmed {
		WriteError(w, fail(403, "local_auth_proof_required", "boolean local confirmation is refused; upgrade agentd and re-pair"))
		return
	}
	if len(in.LocalAuthSignature) > 144 || in.LocalAuthNonce != "" && !attachwatch.LocalAuthNonceValid(in.LocalAuthNonce) || in.Operation != "poll" && (in.LocalAuthSignature != "" || in.LocalAuthNonce != "") {
		WriteError(w, fail(400, "invalid_request", "invalid local confirmation proof"))
		return
	}
	if len(in.MessageLocalAuthSignature) > 144 || in.MessageLocalAuthNonce != "" && !attachwatch.LocalAuthNonceValid(in.MessageLocalAuthNonce) || in.Operation != "message_activate" && (in.MessageLocalAuthSignature != "" || in.MessageLocalAuthNonce != "") {
		WriteError(w, fail(400, "invalid_request", "invalid messaging confirmation proof"))
		return
	}
	if in.Operation == "register" {
		if err := m.registerWatchKey(r.Context(), p, in); err != nil {
			WriteError(w, err)
			return
		}
		reply(w, map[string]any{"state": "registered", "local_consent_proof_version": attachwatch.LocalConsentProofVersion})
		return
	}
	// Authenticate before processing watch IDs, snapshots, leases or text. A
	// readable long-lived lifecycle proof conveys no watch authority.
	m.watchKeys.RLock()
	defer m.watchKeys.RUnlock()
	expected := m.watchKeys.keys[p.TenantID+"/"+in.ComputerID]
	if expected.hash == "" && hashRE.MatchString(in.PollKey) {
		WriteError(w, m.unknownWatchKey(r.Context(), p, in))
		return
	}
	if !hashRE.MatchString(in.PollKey) || subtle.ConstantTimeCompare([]byte(expected.hash), []byte(digest(in.PollKey))) != 1 {
		WriteError(w, fail(403, "forbidden", "daemon poll key rejected"))
		return
	}
	if !uuidRE.MatchString(in.RequestID) {
		WriteError(w, fail(400, "invalid_request", "invalid attach request"))
		return
	}
	if in.Operation == "message_request" || in.Operation == "message_activate" || in.Operation == "message_observed" || in.Operation == "message_offer" || in.Operation == "message_receipt" || in.Operation == "message_validate" {
		m.messageDevice(w, r, p, in, expected)
		return
	}
	if in.Operation != "request" && in.Operation != "poll" && in.Operation != "detach" && in.Operation != "exited" {
		WriteError(w, fail(400, "invalid_request", "invalid attach operation"))
		return
	}
	if in.Operation == "request" {
		if err := m.attachLimit(r.Context(), p.TenantID, "request", 30); err != nil {
			WriteError(w, err)
			return
		}
	}
	var out attachwatch.View
	var rejected error
	var publish string
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		owner, principal, host, workspace, _, err := attachComputer(ctx, tx, in.ComputerID)
		if err != nil {
			// A revoked HTTP bearer remains unauthenticated. This case only
			// gives repair guidance to an authenticated principal for its own
			// retained computer row and registered, memory-only poll key.
			var retainedPrincipal, state string
			if errors.Is(err, pgx.ErrNoRows) && tx.QueryRow(ctx, `SELECT principal_id::text,state FROM agent_pairing_computers WHERE id=$1`, in.ComputerID).Scan(&retainedPrincipal, &state) == nil && retainedPrincipal == p.ID {
				switch state {
				case "revoked":
					return attachFailure(403, "forbidden", "computer proof rejected", attachwatch.RefusalPairing)
				case "draining":
					return attachFailure(403, "forbidden", "computer proof rejected", attachwatch.RefusalDraining)
				}
			}
			return fail(403, "forbidden", "computer proof rejected")
		}
		if principal != p.ID {
			return fail(403, "forbidden", "computer proof rejected")
		}
		if expected.protocol != attachwatch.Protocol {
			return attachFailure(409, "update_agentd", "update agentd to attach protocol 2; fresh approval required", attachwatch.RefusalVersion)
		}
		if expected.proofVersion != attachwatch.LocalConsentProofVersion {
			return attachFailure(409, "update_agentd", "upgrade paimos-agentd to local consent proof v2 and restart; existing pairing keys remain valid; fresh approval required", attachwatch.RefusalVersion)
		}
		if in.Operation != "poll" && (in.Doing != nil || in.ToolActivity != nil) {
			return fail(400, "invalid_request", "activity requires an active watch")
		}
		if in.Operation == "request" {
			s := in.Snapshot
			if in.LocalConfirmed || in.ConsentDigest != "" {
				return fail(400, "invalid_request", "consent is selected by the server at approval")
			}
			if in.Digest != s.Digest() {
				return fail(409, "conflict", "snapshot digest required")
			}
			if !s.Valid() || s.ComputerID != in.ComputerID || !uuidRE.MatchString(s.ProjectID) || !uuidRE.MatchString(s.TicketID) || s.Host != host || !attachwatch.Within(workspace, s.Process.CWD) || in.Text != "" {
				return fail(400, "invalid_request", "invalid snapshot or cwd outside approved workspace")
			}
			if err = attachScope(ctx, tx, p.TenantID, owner, s); err != nil {
				return err
			}
			var n int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM harness_attach_requests WHERE computer_id=$1 AND ((state IN ('pending','approved') AND expires_at>clock_timestamp()) OR (state='active' AND lease_until>clock_timestamp()))`, in.ComputerID).Scan(&n); err != nil {
				return err
			}
			if n >= attachwatch.ComputerMax {
				return fail(429, "rate_limited", "computer attach limit reached")
			}
			v, _, code, e := loadAttach(ctx, tx, in.RequestID)
			if e == nil {
				if v.Digest != s.Digest() {
					return fail(409, "conflict", "attach snapshot is immutable")
				}
				if v.State != "pending" {
					return fail(409, "conflict", "attach request already consumed; create a new request")
				}
				out = v
				out.UserCode = code
				return nil
			}
			if !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			// Admission bound: the pending list shows up to attachwatch.LiveMax
			// waiting or approved requests per person, so none is admitted past it
			// (an existing request above is answered first, so a retry still works).
			// The limits above are fixed windows per tenant and per computer; only
			// this bound is about what a person has to look at. The pairing lock
			// held here makes the count and the insert one step.
			var live int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM harness_attach_requests WHERE owner_id=$1 AND session_id IS NULL AND state IN ('pending','approved') AND expires_at>clock_timestamp()`, owner).Scan(&live); err != nil {
				return err
			}
			if live >= attachwatch.LiveMax {
				return fail(429, attachwatch.LiveLimitCode, attachwatch.LiveLimitMessage)
			}
			num, e := rand.Int(rand.Reader, big.NewInt(1000000000))
			if e != nil {
				return e
			}
			code = fmt.Sprintf("%09d", num.Int64())
			raw, _ := json.Marshal(s)
			_, err = tx.Exec(ctx, `INSERT INTO harness_attach_requests(tenant_id,id,computer_id,owner_id,project_id,ticket_id,snapshot,digest,user_code) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.TenantID, in.RequestID, in.ComputerID, owner, s.ProjectID, s.TicketID, raw, s.Digest(), code)
			if err != nil {
				return err
			}
			out, _, _, err = loadAttach(ctx, tx, in.RequestID)
			out.UserCode = code
			return err
		}
		var approvedOwner string
		out, approvedOwner, _, err = loadAttach(ctx, tx, in.RequestID)
		if err != nil {
			return fail(403, "forbidden", "attach proof rejected")
		}
		if out.Snapshot.ComputerID != in.ComputerID {
			return fail(403, "forbidden", "attach proof rejected")
		}
		if owner != approvedOwner || host != out.Snapshot.Host || !attachwatch.Within(workspace, out.Snapshot.Process.CWD) {
			rejected = fail(403, "forbidden", "attach approval binding changed")
			return attachEnd(ctx, tx, &out, "detached")
		}
		if out.State != "active" && (in.Doing != nil || in.ToolActivity != nil) {
			return fail(400, "invalid_request", "activity requires an active watch")
		}
		waiting := out.State == "pending" || out.State == "approved"
		expired, err := attachExpired(ctx, tx, &out)
		if err != nil {
			return err
		}
		if expired {
			rejected = fail(410, "attach_ended", "watch ended; new approval required")
			if waiting {
				rejected = attachFailure(410, "attach_ended", "watch ended; new approval required", attachwatch.RefusalExpired)
			}
			return nil
		}
		if in.Digest != out.Digest || in.Snapshot.Digest() != out.Digest {
			rejected = fail(409, "conflict", "process or approval snapshot changed")
			return attachEnd(ctx, tx, &out, "detached")
		}
		if in.Operation == "poll" && (out.State == "approved" || out.State == "active") && in.ConsentDigest != out.ConsentDigest {
			// First pending poll may discover approval, but cannot activate it yet.
			if out.State == "approved" && in.ConsentDigest == "" && in.LocalAuthSignature == "" && in.LocalAuthNonce == "" {
				if in.Text != "" {
					rejected = fail(400, "invalid_request", "approval discovery rejects conversation text")
					return attachEnd(ctx, tx, &out, "detached")
				}
				return nil
			}
			rejected = fail(409, "conflict", "consent binding required or mismatched; update agentd and attach again")
			return attachEnd(ctx, tx, &out, "detached")
		}
		if (in.LocalAuthSignature != "" || in.LocalAuthNonce != "") && (out.State != "approved" || out.ConsentMode != attachwatch.ConsentLocalAuth) {
			return fail(403, "local_auth_proof_rejected", "local confirmation challenge already consumed or unavailable")
		}
		// Bind the content prohibition to the saved approval, never just a client flag.
		if out.Snapshot.Mode == attachwatch.ModeLease && in.Text != "" {
			rejected = fail(400, "invalid_request", "metadata-only attach rejects conversation text")
			return attachEnd(ctx, tx, &out, "detached")
		}
		if in.Operation == "detach" {
			return attachEnd(ctx, tx, &out, "detached")
		}
		if in.Operation == "exited" {
			return attachEnd(ctx, tx, &out, "confirmed_exited")
		}
		if err = attachScope(ctx, tx, p.TenantID, owner, out.Snapshot); err != nil {
			rejected = err
			return attachEnd(ctx, tx, &out, "detached")
		}
		if out.State == "pending" {
			if in.Text != "" {
				rejected = fail(400, "invalid_request", "watch not active; conversation text rejected")
				return attachEnd(ctx, tx, &out, "detached")
			}
			return nil
		}
		if out.State == "approved" {
			if out.ConsentMode == attachwatch.ConsentLocalAuth {
				if in.ConsentDigest != "" && in.ConsentDigest != out.ConsentDigest {
					return fail(409, "conflict", "consent binding mismatch")
				}
				if in.LocalAuthSignature == "" {
					if in.LocalAuthNonce != "" {
						return fail(403, "local_auth_proof_required", "signed local confirmation required")
					}
					if in.Text != "" {
						rejected = fail(400, "invalid_request", "local confirmation required before conversation text")
						return attachEnd(ctx, tx, &out, "detached")
					}
					return nil
				}
				var publicKey string
				if err = tx.QueryRow(ctx, `SELECT local_auth_public_key FROM agent_pairing_computers WHERE id=$1`, in.ComputerID).Scan(&publicKey); err != nil {
					return err
				}
				if out.Snapshot.Platform != "darwin" || out.LocalAuthNonce == "" || in.LocalAuthNonce != out.LocalAuthNonce || !attachwatch.VerifyLocalConsent(publicKey, out.ConsentDigest, out.LocalAuthNonce, attachwatch.LocalConsentReason(out.Snapshot), in.LocalAuthSignature) {
					return fail(403, "local_auth_proof_rejected", "signed local confirmation rejected")
				}
				// Consumed in the same transaction as session and lease creation.
				if _, err = tx.Exec(ctx, `UPDATE harness_attach_requests SET local_auth_nonce=NULL WHERE id=$1`, out.RequestID); err != nil {
					return err
				}
				out.LocalAuthNonce = ""
			}
			if in.Text != "" {
				rejected = fail(400, "invalid_request", "activation contains no transcript")
				return attachEnd(ctx, tx, &out, "detached")
			}
			secret, err := randomHex(32)
			if err != nil {
				return err
			}
			var sid string
			err = tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,phase,heartbeat_at) VALUES($1,$2,$3,$4,$5,$6,'unmanaged','worker','scout','{}',$7,$8,'working',clock_timestamp()) RETURNING id::text`, p.TenantID, out.Snapshot.ProjectID, p.ID, out.Snapshot.TicketID, out.Snapshot.Harness, host, []byte(out.Digest), []byte(digest(secret))).Scan(&sid)
			if err != nil {
				return err
			}
			out.SessionID = &sid
			out.State = "active"
		} else {
			var bound bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE id=$1 AND project_id=$2 AND ticket_node_id=$3 AND agent_principal_id=$4 AND harness=$5 AND host=$6 AND stopped_at IS NULL AND archived_at IS NULL)`, *out.SessionID, out.Snapshot.ProjectID, out.Snapshot.TicketID, p.ID, out.Snapshot.Harness, host).Scan(&bound); err != nil {
				return err
			}
			if !bound {
				rejected = fail(410, "attach_ended", "session binding changed or ended")
				return attachEnd(ctx, tx, &out, "detached")
			}
			var previous int64
			var ready bool
			if err = tx.QueryRow(ctx, `SELECT sequence,last_poll<=clock_timestamp()-interval '1 second' FROM harness_attach_requests WHERE id=$1`, out.RequestID).Scan(&previous, &ready); err != nil {
				return err
			}
			if in.Sequence <= previous {
				return fail(409, "conflict", "poll sequence must increase")
			}
			if !ready {
				return fail(429, "rate_limited", "poll at most once per second")
			}
			publish = in.Text
		}
		if in.Sequence < 1 {
			return fail(400, "invalid_request", "poll sequence required")
		}
		if in.Doing != nil || in.ToolActivity != nil {
			if err = agentactivity.ReportAttached(ctx, tx, p.TenantID, p.ID, *out.SessionID, in.Doing, in.ToolActivity); err != nil {
				var invalid *agentactivity.InvalidReport
				if errors.As(err, &invalid) {
					return fail(400, "invalid_request", invalid.Message)
				}
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE harness_sessions SET heartbeat_at=clock_timestamp() WHERE id=$1`, *out.SessionID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `UPDATE harness_attach_requests SET state='active',session_id=$2,sequence=$3,last_poll=clock_timestamp(),lease_until=clock_timestamp()+interval '60 seconds' WHERE id=$1 RETURNING lease_until`, out.RequestID, *out.SessionID, in.Sequence).Scan(&out.LeaseUntil)
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	if rejected != nil {
		WriteError(w, rejected)
		return
	}
	if publish != "" && out.SessionID != nil {
		m.watch.publish(p.TenantID+"/"+*out.SessionID, publish)
	}
	reply(w, out)
}
func (m *Module) attachLookup(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in struct {
		Code string `json:"user_code"`
	}
	if decode(w, r, &in) != nil || len(in.Code) != 9 {
		WriteError(w, fail(400, "invalid_request", "nine-digit code required"))
		return
	}
	if err := m.attachLimit(r.Context(), p.TenantID, "lookup", 10); err != nil {
		WriteError(w, err)
		return
	}
	var out attachwatch.View
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(r.Context(), `SELECT id::text FROM harness_attach_requests WHERE user_code=$1 AND owner_id=$2`, in.Code, p.ID).Scan(&id); err != nil {
			return fail(404, "not_found", "attach unavailable")
		}
		var err error
		out, _, _, err = loadAttach(r.Context(), tx, id)
		if err != nil {
			return err
		}
		_, err = attachExpired(r.Context(), tx, &out)
		return err
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}
func (m *Module) attachApprove(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in struct {
		Digest        string `json:"request_digest"`
		ConsentDigest string `json:"consent_digest"`
	}
	if decode(w, r, &in) != nil {
		WriteError(w, fail(400, "invalid_request", "review digest required"))
		return
	}
	m.attachDecision(w, r, p, in.Digest, in.ConsentDigest, false)
}
func (m *Module) attachRevoke(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	m.attachDecision(w, r, p, "", "", true)
}

// DecideAttachVerified reuses owner, enrollment, immutable consent and local Mac
// confirmation checks. A phone passkey never substitutes for the daemon proof.
func (m *Module) DecideAttachVerified(ctx context.Context, p tenant.Principal, id, d, consentDigest string, revoke bool, verify func(pgx.Tx, attachwatch.View) error) (attachwatch.View, error) {
	if verify == nil || p.Kind != tenant.Person {
		return attachwatch.View{}, fail(403, "forbidden", "fresh person verification required")
	}
	if err := authz.RequirePattern(authz.BindPool(ctx, m.pool), "POST /api/agent-pairing/attach/{requestId}/approve", authz.Scope{}); err != nil {
		return attachwatch.View{}, fail(403, "forbidden", "account management permission required")
	}
	return m.decideAttach(ctx, p, id, d, consentDigest, revoke, verify)
}

func (m *Module) attachDecision(w http.ResponseWriter, r *http.Request, p tenant.Principal, d, consentDigest string, revoke bool) {
	out, err := m.decideAttach(r.Context(), p, r.PathValue("requestId"), d, consentDigest, revoke, nil)
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}

func (m *Module) decideAttach(ctx context.Context, p tenant.Principal, id, d, consentDigest string, revoke bool, verify func(pgx.Tx, attachwatch.View) error) (attachwatch.View, error) {
	var out attachwatch.View
	var rejected error
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		var owner string
		var err error
		out, owner, _, err = loadAttach(ctx, tx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "not_found", "attach unavailable")
		}
		if err != nil {
			return err
		}
		if owner != p.ID {
			return fail(403, "forbidden", "paired computer owner required")
		}
		if verify != nil {
			if err := verify(tx, out); err != nil {
				return err
			}
		}
		if revoke {
			if out.State == "confirmed_exited" {
				return nil
			}
			return attachEnd(ctx, tx, &out, "detached")
		}
		expired, err := attachExpired(ctx, tx, &out)
		if err != nil {
			return err
		}
		if expired {
			rejected = fail(410, "attach_ended", "attach expired")
			return nil
		}
		if verify != nil {
			d = out.Digest
			consentDigest = out.ConsentDigest
		}
		if consentDigest != out.ConsentDigest {
			return fail(409, "conflict", "consent setting changed; review the request again")
		}
		if out.ConsentMode == attachwatch.ConsentLocalAuth && out.Snapshot.Platform != "darwin" {
			return fail(409, "local_auth_unavailable", "local confirmation requires an updated paired Mac daemon; unavailable on Linux")
		}
		if d != out.Digest {
			return fail(409, "conflict", "review digest mismatch")
		}
		owner, _, host, workspace, _, err := attachComputer(ctx, tx, out.Snapshot.ComputerID)
		if err != nil || owner != p.ID || host != out.Snapshot.Host || !attachwatch.Within(workspace, out.Snapshot.Process.CWD) {
			return fail(403, "forbidden", "pairing changed")
		}
		if err = attachScope(ctx, tx, p.TenantID, owner, out.Snapshot); err != nil {
			return err
		}
		if out.State != "pending" {
			return fail(409, "conflict", "approval already consumed")
		}
		if out.State == "pending" {
			var nonce *string
			if out.ConsentMode == attachwatch.ConsentLocalAuth {
				challenge, e := randomHex(32)
				if e != nil {
					return e
				}
				nonce = &challenge
				out.LocalAuthNonce = challenge
			}
			_, err = tx.Exec(ctx, `UPDATE harness_attach_requests SET state='approved',consent_mode=$2,local_auth_nonce=$3 WHERE id=$1`, out.RequestID, out.ConsentMode, nonce)
			out.State = "approved"
		}
		return err
	})
	if err != nil {
		return out, err
	}
	if rejected != nil {
		return out, rejected
	}
	return out, nil
}

// ReviewAttach exposes the immutable request only to the paired computer owner.
func ReviewAttach(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (attachwatch.View, error) {
	v, owner, _, err := loadAttach(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, fail(404, "not_found", "attach unavailable")
	}
	if err != nil {
		return v, err
	}
	if p.Kind != tenant.Person || owner != p.ID {
		return attachwatch.View{}, fail(403, "forbidden", "paired computer owner required")
	}
	if err := authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}); err != nil {
		return attachwatch.View{}, fail(403, "forbidden", "account management permission required")
	}
	if err := attachScope(ctx, tx, p.TenantID, owner, v.Snapshot); err != nil {
		return attachwatch.View{}, err
	}
	v.LocalAuthNonce = ""
	return v, nil
}
