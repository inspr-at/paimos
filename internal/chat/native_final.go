// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// NativeFinalTarget comes only from the native store's CURRENT transaction
// authorizer, after tenant/tree fencing, paired-daemon proof and validation of
// the exact unexpired drain grant, original actor grant, runtime/conversation,
// completed input/turn and current retention consent. It is not a wire DTO.
// A final-only grant can outlive its writer; it confers no ordinary worker lease.
type NativeFinalTarget struct {
	OriginalActor  tenant.Principal // current immutable start actor; never the attributed daemon
	OwnerPersonID  string
	ConversationID string
	RoleID         string
	SessionID      string
	ExpiresAt      time.Time
	Now            func() time.Time
}

func (NativeFinalTarget) MarshalJSON() ([]byte, error) {
	return nil, workorders.Fail(400, "native final target is private")
}
func (*NativeFinalTarget) UnmarshalJSON([]byte) error {
	return workorders.Fail(400, "native final target is private")
}

// NativeFinalWrite is acknowledged only after the enclosing transaction commits.
type NativeFinalWrite struct {
	Receipt nativecontract.NativeFinalReceipt
	result  outboxResult
}

// StoreNativeFinalTx is the narrow chat-isolated storage seam for the new native
// final-drain route. It never mounts a route, changes legacy final/live authority,
// calls VerifyChatLeaseTx with relaxed liveness, or adds a legacy inbox projection.
// The caller MUST hold tenant -> tree fences and validate native authority in
// this same transaction before calling it; all remaining locks precede Append.
func (m *Module) StoreNativeFinalTx(ctx context.Context, tx pgx.Tx, sender tenant.Principal, target NativeFinalTarget, in nativecontract.FinalPublish) (NativeFinalWrite, error) {
	var out NativeFinalWrite
	if m == nil || !m.enabled {
		return out, workorders.Fail(503, "native final chat storage disabled")
	}
	if sender.Kind != tenant.Agent || sender.TenantID != in.ExpectedBinding.TenantID || !workorders.UUID(sender.ID) ||
		target.OriginalActor.TenantID != sender.TenantID || !workorders.UUID(target.OriginalActor.ID) ||
		target.OriginalActor.Kind != tenant.Person && target.OriginalActor.Kind != tenant.Agent ||
		!workorders.UUID(target.OwnerPersonID) || !workorders.UUID(target.ConversationID) || !workorders.UUID(target.RoleID) ||
		target.SessionID != in.ExpectedBinding.HarnessSessionID || !workorders.UUID(in.FinalID) {
		return out, unavailable()
	}
	final := outboxSend{ClientID: "native/" + in.FinalID, Body: in.Text}
	if err := validFinal(final); err != nil {
		return out, err
	}
	// These context IDs reveal no content by themselves. Locks and predicates
	// below re-establish the exact person/role/project even for a stopped writer.
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.chat_role_id',$1,true),set_config('aeon.chat_session_id',$2,true)`, target.RoleID, target.SessionID); err != nil {
		return out, err
	}
	var owner, roleKind string
	if err := tx.QueryRow(ctx, `SELECT owner_person_id::text,kind FROM chat_roles WHERE id=$1 AND project_id=$2 FOR NO KEY UPDATE`, target.RoleID, in.ExpectedBinding.ProjectID).Scan(&owner, &roleKind); err != nil {
		return out, err
	}
	if owner != target.OwnerPersonID {
		return out, unavailable()
	}
	var thread string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM chat_threads WHERE id=$1 AND role_id=$2 AND person_id=$3 AND project_id=$4 AND archived_at IS NULL FOR NO KEY UPDATE`, target.ConversationID, target.RoleID, owner, in.ExpectedBinding.ProjectID).Scan(&thread); err != nil {
		return out, err
	}
	var agent, sessionOwner, project, harnessName, sessionRole string
	var ref, alias []byte
	if err := tx.QueryRow(ctx, `SELECT agent_principal_id::text,owner_principal_id::text,project_id::text,harness,role,ref_digest,vendor_ref_digest FROM harness_sessions WHERE id=$1 AND management='managed' AND capabilities @> ARRAY['chat']::text[] FOR NO KEY UPDATE`, target.SessionID).Scan(&agent, &sessionOwner, &project, &harnessName, &sessionRole, &ref, &alias); err != nil {
		return out, err
	}
	if agent != sender.ID || sessionOwner != owner || project != in.ExpectedBinding.ProjectID ||
		roleKind == "lead" && sessionRole != "coordinator" || roleKind == "worker" && sessionRole != "worker" {
		return out, unavailable()
	}
	if sessionRole == "coordinator" {
		var canonical string
		if target.OriginalActor.Kind != tenant.Person {
			return out, unavailable()
		}
		if err := tx.QueryRow(ctx, `SELECT `+modelprefs.CanonicalPersonSQL("id")+`::text FROM principals WHERE id=$1 AND kind='person' AND status='active'`, target.OriginalActor.ID).Scan(&canonical); err != nil {
			return out, err
		}
		if canonical != owner {
			return out, unavailable()
		}
	}
	var contextMatches bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_session_contexts WHERE session_id=$1 AND role_id=$2 AND owner_person_id=$3 AND project_id=$4 AND harness=$5 AND ref_digest=$6)`, target.SessionID, target.RoleID, owner, project, harnessName, ref).Scan(&contextMatches); err != nil {
		return out, err
	}
	if !contextMatches {
		return out, unavailable()
	}
	for _, digest := range [][]byte{ref, alias} {
		if len(digest) == 0 {
			continue
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_native_contexts WHERE harness=$1 AND ref_digest=$2 AND role_id=$3 AND owner_person_id=$4 AND project_id=$5)`, harnessName, digest, target.RoleID, owner, project).Scan(&contextMatches); err != nil {
			return out, err
		}
		if !contextMatches {
			return out, unavailable()
		}
	}
	// CURRENT target visibility and permissions are checked inside the final
	// write, never borrowed from an earlier claim or network response.
	scope := authz.Scope{ProjectID: project}
	if err := authz.RequireTx(ctx, tx, sender, "harness.worker", scope); err != nil {
		return out, err
	}
	for _, permission := range []string{"harness.control", "chat.receive", "chat.send"} {
		if err := authz.RequireTx(ctx, tx, target.OriginalActor, permission, scope); err != nil {
			return out, err
		}
	}
	person := tenant.Principal{TenantID: sender.TenantID, ID: owner, Kind: tenant.Person}
	if err := authz.RequireTx(ctx, tx, person, "chat.read", scope); err != nil {
		return out, err
	}
	if err := authz.RequireTx(ctx, tx, person, "nodes.read", scope); err != nil {
		return out, err
	}
	var visible string
	if err := tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n WHERE n.id=$1 AND n.deleted_at IS NULL`, project).Scan(&visible); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.chat_conversation_id',$1,true)`, thread); err != nil {
		return out, err
	}
	// Pre-acquire every lock storeFinal will need, then check the publication
	// deadline at the actual final write boundary. A stopped writer cannot
	// reuse an unrelated legacy final with a colliding client message ID.
	var oldSession string
	err := tx.QueryRow(ctx, `SELECT coalesce(sender_session_id::text,'') FROM inbox_messages WHERE chat_thread_id=$1 AND sender_principal_id=$2 AND idempotency_key=$3 FOR NO KEY UPDATE`, thread, sender.ID, "chat/"+thread+"/"+final.ClientID).Scan(&oldSession)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if err == nil && oldSession != target.SessionID {
		return out, workorders.Fail(409, "native final message generation conflicts")
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM principals WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, []string{sender.ID, owner}); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM nodes WHERE id=$1 FOR KEY SHARE`, project); err != nil {
		return out, err
	}
	now := target.Now
	if now == nil {
		now = time.Now
	}
	if target.ExpiresAt.IsZero() || !now().Before(target.ExpiresAt) {
		return out, &nativecontract.ValidationError{Reason: nativecontract.ReasonPublicationGrantExpired, Path: "$.drain_grant_id"}
	}
	stored, err := storeFinal(ctx, tx, sender, thread, project, sender.ID, owner, target.SessionID, "", final)
	if err != nil {
		return out, err
	}
	out.result = stored
	id := stored.Message.ID
	out.Receipt = nativecontract.NativeFinalReceipt{FinalID: in.FinalID, MessageID: &id, State: "persisted", UpdatedAt: now().UTC()}
	return out, nil
}

func (m *Module) NativeFinalCommitted(write NativeFinalWrite) { write.result.afterCommit(m) }
