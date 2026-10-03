// SPDX-License-Identifier: AGPL-3.0-only
package attachedmsg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,80}$`)

type Attachment struct {
	ID, Session, Computer, Owner, Project, Principal, Digest, Consent, State string
	LocalAuthPublicKey                                                       string
	Snapshot                                                                 attachwatch.Snapshot
	Lease                                                                    time.Time
}

// AttachmentForSend identifies attachments from server records, never a mode
// flag. Omitting session for a principal with a live attachment is refused.
func AttachmentForSend(ctx context.Context, tx pgx.Tx, recipient string, session *string) (string, error) {
	var id string
	if session == nil {
		var attached bool
		e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_attach_requests a JOIN agent_pairing_computers c ON c.tenant_id=a.tenant_id AND c.id=a.computer_id WHERE c.principal_id=$1::uuid AND a.state='active' AND a.lease_until>clock_timestamp())`, recipient).Scan(&attached)
		if e != nil {
			return "", e
		}
		if attached {
			return "", Fail(409, "attached_session_required")
		}
		return "", nil
	}
	e := tx.QueryRow(ctx, `SELECT id::text FROM harness_attach_requests WHERE session_id=$1::uuid AND state='active' AND lease_until>clock_timestamp()`, *session).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", nil
	}
	return id, e
}
func LoadAttachment(ctx context.Context, tx pgx.Tx, id string) (Attachment, error) {
	// Caller holds the complete Lock prefix before any attachment/grant row.
	var a Attachment
	var live bool
	var currentOwner, host, workspace, principal string
	var raw []byte
	var lease *time.Time
	e := tx.QueryRow(ctx, `SELECT a.id::text,a.session_id::text,a.computer_id::text,a.owner_id::text,a.project_id::text,a.digest,a.consent_mode,a.state,a.lease_until,a.snapshot,
 q.approved_by::text,q.details->>'computer_name',q.details->>'workspace_path',c.principal_id::text,c.local_auth_public_key,
 a.state='active' AND a.lease_until>clock_timestamp() AND c.state='connected' AND q.state='redeemed'
 FROM harness_attach_requests a JOIN agent_pairing_computers c ON c.tenant_id=a.tenant_id AND c.id=a.computer_id JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 WHERE a.id=$1::uuid AND a.session_id IS NOT NULL FOR UPDATE OF a`, id).Scan(&a.ID, &a.Session, &a.Computer, &a.Owner, &a.Project, &a.Digest, &a.Consent, &a.State, &lease, &raw, &currentOwner, &host, &workspace, &principal, &a.LocalAuthPublicKey, &live)
	if errors.Is(e, pgx.ErrNoRows) {
		return a, Fail(409, "attachment_unavailable")
	}
	if e != nil {
		return a, e
	}
	if !live || lease == nil {
		return a, Fail(409, "attachment_offline")
	}
	if json.Unmarshal(raw, &a.Snapshot) != nil || a.Owner != currentOwner || a.Digest != a.Snapshot.Digest() || host != a.Snapshot.Host || !attachwatch.Within(workspace, a.Snapshot.Process.CWD) {
		return a, Fail(409, "attachment_binding_changed")
	}
	a.Lease = *lease
	a.Principal = principal
	var bound bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE id=$1::uuid AND project_id=$2::uuid AND agent_principal_id=$3::uuid AND harness=$4 AND host=$5 AND ticket_node_id=$7::uuid AND stopped_at IS NULL AND archived_at IS NULL)
 AND EXISTS(SELECT 1 FROM agent_pairing_enrollments e JOIN agent_accounts ac ON ac.tenant_id=e.tenant_id AND ac.id=e.account_id WHERE e.computer_id=$6::uuid AND e.state='connected' AND ac.harness=$4)
 AND EXISTS(SELECT 1 FROM nodes p JOIN node_kinds k ON k.tenant_id=p.tenant_id AND k.id=p.kind_id JOIN nodes t ON t.tenant_id=p.tenant_id AND t.project_id=p.id WHERE p.id=$2::uuid AND k.slug='project' AND p.deleted_at IS NULL AND t.id=$7::uuid AND t.deleted_at IS NULL)`, a.Session, a.Project, principal, a.Snapshot.Harness, host, a.Computer, a.Snapshot.TicketID).Scan(&bound)
	if e != nil {
		return a, e
	}
	if !bound {
		return a, Fail(409, "attachment_binding_changed")
	}
	owner := tenant.Principal{ID: a.Owner, TenantID: tenantID(ctx), Kind: tenant.Person}
	// Use the actual tenant from the transaction; callers need not carry an actor
	// (the paired machine is not the person whose authorization must be rechecked).
	if e = tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&owner.TenantID); e != nil {
		return a, e
	}
	for _, perm := range []string{"account.manage", "harness.write", "inbox.send"} {
		if authz.RequireTx(ctx, tx, owner, perm, authz.Scope{ProjectID: a.Project}) != nil {
			return a, Fail(403, "owner_authority_lost")
		}
	}
	return a, nil
}
func tenantID(ctx context.Context) string { p, _ := tenant.PrincipalFrom(ctx); return p.TenantID }
func loadGrant(ctx context.Context, tx pgx.Tx, request string) (Grant, error) {
	var g Grant
	e := tx.QueryRow(ctx, `SELECT g.id::text,g.binding,g.state,g.consent_digest,CASE WHEN g.state='active' THEN a.lease_until ELSE g.expires_at END,g.hook_observed_at,a.snapshot,coalesce(g.local_auth_nonce,'') FROM attached_message_grants g JOIN harness_attach_requests a ON a.tenant_id=g.tenant_id AND a.id=g.attach_request_id WHERE g.attach_request_id=$1::uuid AND g.state<>'revoked' FOR UPDATE OF g`, request).Scan(&g.ID, &g.Binding, &g.State, &g.Digest, &g.ExpiresAt, &g.ObservedAt, &g.Snapshot, &g.LocalAuthNonce)
	g.Notice = consentNotice
	return g, e
}

const consentNotice = "Aeon does not retain note text. The harness may retain it in its transcript and send it to its model provider. Authorized live watchers may see the session repeat it; status-only attachments have no conversation watch. Processes running as your OS user are not isolated."

func (s *Service) hook(ctx context.Context, tx pgx.Tx, a Attachment, version string) error {
	c, e := s.capability(ctx, tx, a.Computer, a.Snapshot.Harness)
	if e != nil {
		return e
	}
	if !c.Verified {
		if c.Blocker == "" {
			c.Blocker = "repair_required"
		}
		return Fail(409, c.Blocker)
	}
	if version != "" && c.Version != version {
		return Fail(409, "hook_version_changed")
	}
	return nil
}
func (s *Service) validate(ctx context.Context, tx pgx.Tx, a Attachment, g Grant) error {
	if !s.Enabled() {
		return Fail(409, "feature_disabled")
	}
	b := g.Binding
	if b.TenantID != tenantID(ctx) || b.AttachRequestID != a.ID || b.SessionID != a.Session || b.ProjectID != a.Project || b.ComputerID != a.Computer || b.OwnerID != a.Owner || b.SnapshotDigest != a.Digest || b.Scope != "owner_messages" || b.ServiceEpoch != s.epoch || b.ConsentPolicy != a.Consent || b.Digest() != g.Digest {
		return Fail(409, "message_binding_changed")
	}
	var policy string
	var saved bool
	if e := tx.QueryRow(ctx, `SELECT coalesce((SELECT consent_mode FROM person_watch_security WHERE person_id=$1::uuid),'aeon'),EXISTS(SELECT 1 FROM person_watch_security WHERE person_id=$1::uuid)`, a.Owner).Scan(&policy, &saved); e != nil {
		return e
	}
	if a.LocalAuthPublicKey != "" && (!saved || policy == attachwatch.ConsentLocalAuth) {
		policy = attachwatch.ConsentLocalAuth
	}
	// An explicit local_auth choice changing since approval invalidates a grant,
	// even on an old computer that needs repair before that policy can work.
	if policy != b.ConsentPolicy {
		return Fail(409, "consent_policy_changed")
	}
	if g.State != "active" {
		var expired bool
		if e := tx.QueryRow(ctx, `SELECT $1::timestamptz<=clock_timestamp()`, g.ExpiresAt).Scan(&expired); e != nil {
			return e
		}
		if expired {
			return Fail(409, "message_consent_expired")
		}
	}
	return s.hook(ctx, tx, a, b.HarnessVersion)
}

// ValidateGrant is the integration entrypoint for S2-3 offers/receipts. Caller
// holds Lock; equality includes every nonce-binding field and current authority.
func (s *Service) ValidateGrant(ctx context.Context, tx pgx.Tx, expected Binding) (Grant, error) {
	a, e := LoadAttachment(ctx, tx, expected.AttachRequestID)
	if e != nil {
		return Grant{}, e
	}
	g, e := loadGrant(ctx, tx, a.ID)
	if e != nil {
		return g, Fail(409, "messages_off")
	}
	if g.Binding != expected {
		return g, Fail(409, "message_binding_changed")
	}
	if e = s.validate(ctx, tx, a, g); e != nil {
		return g, e
	}
	if g.State != "active" || g.ObservedAt == nil {
		return g, Fail(409, "waiting_for_hook")
	}
	return g, nil
}

type Prepare struct{ ReleaseDigest, ConfigDigest, Version, DaemonEpoch string }

func (s *Service) Prepare(ctx context.Context, tx pgx.Tx, request string, in Prepare) (Grant, error) {
	if !s.Enabled() {
		return Grant{}, Fail(409, "feature_disabled")
	}
	if !hashPattern.MatchString(in.ReleaseDigest) || !hashPattern.MatchString(in.ConfigDigest) || !hashPattern.MatchString(in.DaemonEpoch) || !versionPattern.MatchString(in.Version) {
		return Grant{}, Fail(400, "invalid_message_binding")
	}
	a, e := LoadAttachment(ctx, tx, request)
	if e != nil {
		return Grant{}, e
	}
	if e = s.hook(ctx, tx, a, in.Version); e != nil {
		return Grant{}, e
	}
	if prior, err := loadGrant(ctx, tx, request); err == nil {
		var expired bool
		if err = tx.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() AND state IN ('pending','approved') FROM attached_message_grants WHERE id=$1`, prior.ID).Scan(&expired); err != nil {
			return Grant{}, err
		}
		if !expired {
			return Grant{}, Fail(409, "message_consent_already_pending")
		}
		if _, err = tx.Exec(ctx, `UPDATE attached_message_grants SET state='revoked' WHERE id=$1`, prior.ID); err != nil {
			return Grant{}, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, err
	}
	g := Grant{ID: UUID(), State: "pending", Snapshot: a.Snapshot, Notice: consentNotice}
	g.Binding = Binding{TenantID: tenantID(ctx), ProjectID: a.Project, AttachRequestID: a.ID, SessionID: a.Session, ComputerID: a.Computer, OwnerID: a.Owner, Generation: UUID(), DaemonEpoch: in.DaemonEpoch, HookReleaseDigest: in.ReleaseDigest, HookConfigDigest: in.ConfigDigest, HarnessVersion: in.Version, Scope: "owner_messages", ConsentPolicy: a.Consent, SnapshotDigest: a.Digest, ServiceEpoch: s.epoch}
	g.Digest = g.Binding.Digest()
	raw, _ := json.Marshal(g.Binding)
	e = tx.QueryRow(ctx, `INSERT INTO attached_message_grants(tenant_id,id,attach_request_id,session_id,computer_id,owner_id,project_id,message_generation,binding,consent_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING expires_at`, g.Binding.TenantID, g.ID, a.ID, a.Session, a.Computer, a.Owner, a.Project, g.Binding.Generation, raw, g.Digest).Scan(&g.ExpiresAt)
	return g, e
}
func (s *Service) Approve(ctx context.Context, tx pgx.Tx, p tenant.Principal, request, generation, digest string) (Grant, error) {
	if !interactive(ctx) || p.Kind != tenant.Person {
		return Grant{}, Fail(403, "interactive_owner_required")
	}
	a, e := LoadAttachment(ctx, tx, request)
	if e != nil {
		return Grant{}, e
	}
	if p.ID != a.Owner {
		return Grant{}, Fail(403, "computer_owner_required")
	}
	g, e := loadGrant(ctx, tx, request)
	if e != nil {
		return g, Fail(409, "messages_off")
	}
	if e = s.validate(ctx, tx, a, g); e != nil {
		return g, e
	}
	if g.State != "pending" || generation != g.Binding.Generation || digest != g.Digest {
		return g, Fail(409, "message_consent_mismatch")
	}
	if g.Binding.ConsentPolicy == attachwatch.ConsentLocalAuth {
		if a.Snapshot.Platform != "darwin" || attachwatch.LocalAuthPublicKey(a.LocalAuthPublicKey) == nil {
			return g, Fail(409, "local_confirmation_unavailable")
		}
		var challenge [32]byte
		if _, e = rand.Read(challenge[:]); e != nil {
			return g, e
		}
		g.LocalAuthNonce = hex.EncodeToString(challenge[:])
	}
	_, e = tx.Exec(ctx, `UPDATE attached_message_grants SET state='approved',local_auth_nonce=nullif($2,'') WHERE id=$1`, g.ID, g.LocalAuthNonce)
	g.State = "approved"
	return g, e
}

// Activate/Observe run only behind the registered protocol-2 poll key. Observe
// is S2-4's fresh peer-binding report, never a vendor reference or old inbox beat.
func (s *Service) Activate(ctx context.Context, tx pgx.Tx, request, generation, digest, epoch, release, config, version string, nonce, signature string, observed bool) (Grant, error) {
	a, e := LoadAttachment(ctx, tx, request)
	if e != nil {
		return Grant{}, e
	}
	g, e := loadGrant(ctx, tx, request)
	if e != nil {
		return g, Fail(409, "messages_off")
	}
	if e = s.validate(ctx, tx, a, g); e != nil {
		return g, e
	}
	if generation != g.Binding.Generation || digest != g.Digest || epoch != g.Binding.DaemonEpoch || release != g.Binding.HookReleaseDigest || config != g.Binding.HookConfigDigest || version != g.Binding.HarnessVersion {
		return g, Fail(409, "message_binding_changed")
	}
	if observed {
		if nonce != "" || signature != "" {
			return g, Fail(400, "unexpected_message_consent_proof")
		}
		if g.State != "active" {
			return g, Fail(409, "messages_off")
		}
		_, e = tx.Exec(ctx, `UPDATE attached_message_grants SET hook_observed_at=clock_timestamp() WHERE id=$1`, g.ID)
	} else {
		if g.State != "approved" {
			return g, Fail(409, "message_consent_mismatch")
		}
		if g.Binding.ConsentPolicy == attachwatch.ConsentLocalAuth {
			if a.Snapshot.Platform != "darwin" || nonce != g.LocalAuthNonce || !VerifyLocalConsent(a.LocalAuthPublicKey, g.Digest, nonce, LocalConsentReason(g), signature) {
				return g, Fail(403, "message_local_auth_proof_required")
			}
		} else if nonce != "" || signature != "" {
			return g, Fail(400, "unexpected_message_consent_proof")
		}
		_, e = tx.Exec(ctx, `UPDATE attached_message_grants SET state='active',local_auth_nonce=NULL WHERE id=$1`, g.ID)
	}
	if e != nil {
		return g, e
	}
	return loadGrant(ctx, tx, request)
}
func (s *Service) Project(ctx context.Context, tx pgx.Tx, p tenant.Principal, request string) (Capability, error) {
	// Check owner even when off/offline; capability/history is never tenant-admin visible.
	var owner string
	if e := tx.QueryRow(ctx, `SELECT owner_id::text FROM harness_attach_requests WHERE id=$1::uuid`, request).Scan(&owner); e != nil {
		return Capability{}, Fail(404, "not_found")
	}
	if p.Kind != tenant.Person || p.ID != owner || !p.BrowserSession {
		return Capability{}, Fail(403, "computer_owner_required")
	}
	out := Capability{}
	if !s.Enabled() {
		out.Blocker = "feature_disabled"
		return out, nil
	}
	a, e := LoadAttachment(ctx, tx, request)
	if e != nil {
		if f, ok := e.(*Error); ok {
			out.Blocker = f.Code
			return out, nil
		}
		return out, e
	}
	if e = s.hook(ctx, tx, a, ""); e != nil {
		if f, ok := e.(*Error); ok {
			out.Blocker = f.Code
			return out, nil
		}
		return out, e
	}
	g, e := loadGrant(ctx, tx, request)
	if errors.Is(e, pgx.ErrNoRows) {
		out.Blocker = "messages_off"
		return out, nil
	}
	if e != nil {
		return out, e
	}
	out.Grant = &g
	if e = s.validate(ctx, tx, a, g); e != nil {
		if f, ok := e.(*Error); ok {
			out.Blocker = f.Code
			return out, nil
		}
		return out, e
	}
	switch {
	case g.State == "pending":
		out.Blocker = "owner_consent_required"
	case g.State == "approved":
		out.Blocker = "local_activation_required"
	case g.ObservedAt == nil:
		out.Blocker = "waiting_for_hook"
	default:
		out.Ready = true
	}
	if g.State == "active" {
		out.Grant.ExpiresAt = a.Lease
	}
	return out, nil
}
