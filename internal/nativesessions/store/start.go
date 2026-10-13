// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func authenticated(ctx context.Context, actor nativecontract.Actor) (tenant.Principal, error) {
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok || p.ID != actor.PrincipalID || p.TenantID != actor.TenantID || string(p.Kind) != actor.PrincipalKind || !slices.Equal(p.Scopes, actor.KeyCeiling) {
		return tenant.Principal{}, fail(403, nativecontract.ReasonWrongPrincipal)
	}
	if !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
		return tenant.Principal{}, fail(403, nativecontract.ReasonWrongPrincipal)
	}
	if p.Kind != tenant.Person && p.Kind != tenant.Agent {
		return tenant.Principal{}, fail(403, nativecontract.ReasonWrongPrincipal)
	}
	if p.Kind == tenant.Agent && p.KeyID == "" {
		return tenant.Principal{}, fail(403, nativecontract.ReasonGrantMissing)
	}
	return p, nil
}

func requireStart(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) error {
	for _, permission := range []string{"harness.control", "run.create"} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
			return fail(403, nativecontract.ReasonPermissionRevoked)
		}
	}
	return nil
}

func (s *Store) authorize(ctx context.Context, tx pgx.Tx, actor nativecontract.Actor, project string, intent nativecontract.StartIntent) (Authorization, error) {
	a, err := s.authority.AuthorizeTx(ctx, tx, actor, project, intent)
	if err != nil {
		return a, err
	}
	if a.ProjectID != project || !workorders.UUID(a.OwnerPersonID) {
		return a, fail(403, nativecontract.ReasonGrantMissing)
	}
	if intent.Role == "coordinator" && (actor.PrincipalKind != "person" || !a.PersonInvoked || a.RunID != nil) {
		return a, fail(403, nativecontract.ReasonWrongPrincipal)
	}
	if intent.Role == "coordinator" {
		var canonical string
		if err = tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE id=$1 AND status='active' AND kind='person'`, actor.PrincipalID).Scan(&canonical); err != nil {
			return a, fail(403, nativecontract.ReasonWrongPrincipal)
		}
		if canonical != a.OwnerPersonID {
			return a, fail(403, nativecontract.ReasonWrongPrincipal)
		}
	}
	if intent.Role == "worker" && (a.RunID == nil || !workorders.UUID(*a.RunID)) {
		return a, fail(403, nativecontract.ReasonGrantMissing)
	}
	if actor.OwnerPersonID != "" && actor.OwnerPersonID != a.OwnerPersonID {
		return a, fail(403, nativecontract.ReasonWrongPrincipal)
	}
	if err = s.validate("NativePolicy", a.Policy); err != nil {
		return a, err
	}
	if err = s.validate("NativeLaunchConfig", a.Launch); err != nil {
		return a, err
	}
	if err = s.validate("NativeCapabilities", a.Capabilities); err != nil {
		return a, err
	}
	if !s.now().Before(a.Policy.ExpiresAt) {
		return a, fail(403, nativecontract.ReasonGrantExpired)
	}
	if a.Launch.ModelProfileID != intent.ModelProfileID || a.Launch.ToolPolicyDigest != a.Policy.ToolPolicyDigest || a.Launch.SettingsPolicyDigest != a.Policy.SettingsPolicyDigest {
		return a, fail(403, nativecontract.ReasonIdentityMismatch)
	}
	return a, nil
}

func (s *Store) Start(ctx context.Context, actor nativecontract.Actor, intent nativecontract.StartIntent) (nativecontract.OperationResult, error) {
	var out nativecontract.OperationResult
	if !startBounds(intent) {
		return out, fail(413, nativecontract.ReasonBufferFull)
	}
	if err := s.validate("NativeStartIntent", intent); err != nil {
		return out, err
	}
	project := projectFrom(ctx)
	if !workorders.UUID(project) {
		return out, fail(400, nativecontract.ReasonInvalidInput)
	}
	if intent.ResumeFromOwnedSessionID != nil {
		return out, fail(400, nativecontract.ReasonUnsupported)
	}
	p, err := authenticated(ctx, actor)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := requireStart(ctx, tx, p, project); err != nil {
			return err
		}
		a, err := s.authorize(ctx, tx, actor, project, intent)
		if err != nil {
			return err
		}
		r := actorRecord{Principal: p, KeyID: p.KeyID, AuthKeyID: p.AuthKeyID, ActorOwnerID: a.OwnerPersonID, GrantID: a.Policy.GrantID, GrantRevision: a.Policy.GrantRevision}
		intentRaw, _ := json.Marshal(intent)
		actorRaw, _ := json.Marshal(r)
		digest := requestDigest(project, p.ID, intentRaw)
		var oldDigest, oldActor, viewRaw []byte
		var operation string
		var at time.Time
		err = tx.QueryRow(ctx, `SELECT i.id::text,i.request_digest,i.actor_authority,b.view,i.created_at FROM native_session_intents i
 JOIN native_session_bindings b ON b.tenant_id=i.tenant_id AND b.id=i.owned_session_id WHERE i.request_id=$1`, intent.RequestID).Scan(&operation, &oldDigest, &oldActor, &viewRaw, &at)
		if err == nil {
			// A request ID cannot be rebound to another actor/key/ceiling/grant.
			if !bytes.Equal(oldDigest, digest) || !jsonEqual(oldActor, actorRaw) {
				return fail(409, nativecontract.ReasonIdentityMismatch)
			}
			var view nativecontract.SessionView
			if err = json.Unmarshal(viewRaw, &view); err != nil {
				return err
			}
			out = operationResult(intent.RequestID, operation, view, at, p.Kind)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err = targetStart(ctx, tx, project, intent, true); err != nil {
			return err
		}
		var principal, daemon string
		err = tx.QueryRow(ctx, `SELECT c.principal_id::text,c.daemon_id FROM agent_pairing_computers c
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 WHERE c.id=$1 AND c.state='connected' AND q.state='redeemed' AND q.approved_by=$2`, intent.ComputerID, a.OwnerPersonID).Scan(&principal, &daemon)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(403, nativecontract.ReasonWrongPrincipal)
		}
		if err != nil {
			return err
		}
		var own bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_accounts a JOIN agent_pairing_enrollments e ON e.tenant_id=a.tenant_id AND e.account_id=a.id
 WHERE a.id=$1 AND a.owner_person_id=$2 AND e.computer_id=$3 AND e.state='connected' AND e.ongoing_approved_at IS NOT NULL AND a.harness=$4)`, intent.AccountID, a.OwnerPersonID, intent.ComputerID, intent.Harness).Scan(&own); err != nil {
			return err
		}
		if !own {
			return fail(403, nativecontract.ReasonWrongPrincipal)
		}
		session, err := uuid()
		if err != nil {
			return err
		}
		operation, err = uuid()
		if err != nil {
			return err
		}
		view := nativecontract.SessionView{OwnedSessionID: session, OwnerPersonID: a.OwnerPersonID, Role: intent.Role, Profile: "native", Harness: intent.Harness, State: "accepted", Mode: "none", Capabilities: a.Capabilities, Revision: 1, Generation: 1, ConversationRef: session}
		at = s.now()
		viewRaw, _ = json.Marshal(view)
		policyRaw, _ := json.Marshal(a.Policy)
		launchRaw, _ := json.Marshal(a.Launch)
		_, err = tx.Exec(ctx, `INSERT INTO native_session_bindings(tenant_id,id,project_id,owner_person_id,actor_principal_id,role,harness,account_id,computer_id,paired_principal_id,workspace_id,model_profile_id,work_id,parent_harness_session_id,policy_id,daemon_id,binding_revision,view,created_at,run_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,1,$17,$18,$19)`, p.TenantID, session, project, a.OwnerPersonID, p.ID, intent.Role, intent.Harness, intent.AccountID, intent.ComputerID, principal, intent.WorkspaceID, intent.ModelProfileID, intent.WorkID, intent.ParentHarnessSessionID, intent.PolicyID, daemon, viewRaw, at, a.RunID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO native_session_intents(tenant_id,id,request_id,owned_session_id,project_id,owner_person_id,actor_principal_id,actor_kind,actor_key_id,actor_key_ceiling,actor_authority,actor_grant_id,actor_grant_revision,request_digest,intent,policy,launch_config,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, p.TenantID, operation, intent.RequestID, session, project, a.OwnerPersonID, p.ID, string(p.Kind), p.KeyID, p.Scopes, actorRaw, a.Policy.GrantID, a.Policy.GrantRevision, digest, intentRaw, policyRaw, launchRaw, at)
		if err != nil {
			return err
		}
		out = operationResult(intent.RequestID, operation, view, at, p.Kind)
		return appendEvent(ctx, tx, p, "native_session.start_requested", project, operation, intent.RequestID, view)
	})
	if err != nil {
		return nativecontract.OperationResult{}, err
	}
	return out, nil
}

func startBounds(in nativecontract.StartIntent) bool {
	for _, v := range []string{in.RequestID, in.ComputerID, in.AccountID, in.ModelProfileID, in.PolicyID} {
		if len(v) > 36 {
			return false
		}
	}
	for _, v := range []*string{in.WorkID, in.ParentHarnessSessionID, in.ResumeFromOwnedSessionID} {
		if v != nil && len(*v) > 36 {
			return false
		}
	}
	return len(in.WorkspaceID) <= 128 && len(in.Role) <= 32 && len(in.Profile) <= 32 && len(in.Harness) <= 32
}

func targetStart(ctx context.Context, tx pgx.Tx, project string, intent nativecontract.StartIntent, checkRevision bool) error {
	var rev int64
	var archived bool
	// C0's project revision is the existing lead projection coordinate (zero
	// before a lead intent exists), never a fabricated node timestamp/version.
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT l.revision FROM project_leads l WHERE l.project_id=n.id),0),n.deleted_at IS NOT NULL FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND k.slug='project'`, project).Scan(&rev, &archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, nativecontract.ReasonInvalidInput)
	}
	if err != nil {
		return err
	}
	if archived {
		return fail(404, nativecontract.ReasonInvalidInput)
	}
	if checkRevision && rev != intent.ExpectedProjectRevision {
		return fail(409, nativecontract.ReasonStaleRevision)
	}
	if intent.Role == "worker" {
		var leaf bool
		err = tx.QueryRow(ctx, `SELECT n.project_id=$2 AND n.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM nodes child WHERE child.parent_id=n.id AND child.deleted_at IS NULL) FROM nodes n WHERE n.id=$1`, *intent.WorkID, project).Scan(&leaf)
		if err != nil {
			return err
		}
		if !leaf {
			return fail(409, nativecontract.ReasonInvalidInput)
		}
		if _, err = tx.Exec(ctx, `SELECT aeon_require_work_leaf($1::uuid)`, *intent.WorkID); err != nil {
			return fail(409, nativecontract.ReasonInvalidInput)
		}
		var parent bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE id=$1 AND project_id=$2 AND stopped_at IS NULL AND archived_at IS NULL AND role='coordinator')`, *intent.ParentHarnessSessionID, project).Scan(&parent); err != nil {
			return err
		}
		if !parent {
			return fail(409, nativecontract.ReasonWrongGeneration)
		}
	}
	return nil
}

func requestDigest(project, actor string, intent []byte) []byte {
	sum := sha256.Sum256(append([]byte(project+"\x00"+actor+"\x00"), intent...))
	return sum[:]
}
func jsonEqual(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}
func uuid() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

func operationResult(request, operation string, view nativecontract.SessionView, at time.Time, kind tenant.PrincipalKind) nativecontract.OperationResult {
	view = nativecontract.PersonProjection(view, string(kind), true)
	r := nativecontract.Receipt{RequestID: request, OperationID: operation, Binding: view.CurrentBinding, Operation: "start", State: "accepted", SourceSequence: "0", UpdatedAt: at}
	view.LastReceipt = &r
	return nativecontract.OperationResult{Receipt: r, Session: &view}
}

func appendEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, project, operation, request string, view nativecontract.SessionView) error {
	var harness, epoch *string
	if view.CurrentBinding != nil {
		harness = &view.CurrentBinding.HarnessSessionID
		epoch = &view.CurrentBinding.RuntimeEpoch
	}
	_, err := events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: kind, After: map[string]any{"project_id": project, "owned_session_id": view.OwnedSessionID, "harness_session_id": harness, "runtime_epoch": epoch, "revision": view.Revision, "request_id": request, "state": view.State, "reason": view.Reason, "operation_id": operation}})
	return err
}
