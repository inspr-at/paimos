// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var errAssistantLimit = errors.New("owner already has three active messaging assistants")
var errAssistantName = errors.New("name is already in use")
var errAssistantInput = errors.New("name and a future expiry within 30 days are required")

type assistantInput struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (m *Module) handleMessagingAssistant(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w)
		return
	}
	operation := "create"
	id := r.PathValue("principalId")
	if id != "" {
		if !uuidRe.MatchString(id) {
			writeBadRequest(w, "invalid principalId")
			return
		}
		operation = "rotate"
		if r.Method == http.MethodDelete {
			operation = "revoke"
		}
	}
	var body assistantInput
	if operation != "revoke" {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var extra any
		if err := dec.Decode(&body); err != nil {
			writeBadRequest(w, "invalid assistant request")
			return
		}
		if dec.Decode(&extra) != io.EOF || operation == "rotate" && body.Name != "" {
			writeBadRequest(w, "invalid assistant request")
			return
		}
	}
	rec, err := m.messagingAssistant(r.Context(), p, operation, id, body)
	switch {
	case errors.Is(err, authz.ErrForbidden), errors.Is(err, db.ErrKeyAuthorityChanged):
		writeForbidden(w)
	case errors.Is(err, errAssistantInput):
		writeBadRequest(w, errAssistantInput.Error())
	case errors.Is(err, errAssistantLimit), errors.Is(err, errAssistantName):
		writeJSON(w, 409, errorJSON{Error: err.Error()})
	case errors.Is(err, errNotFound):
		writeJSON(w, 404, errorJSON{Error: "owned messaging assistant not found"})
	case errors.Is(err, errAgentDeactivated):
		writeJSON(w, 409, errorJSON{Error: "assistant is deactivated"})
	case err != nil:
		internalFailure(w, r, "messaging_assistant", err)
	case operation == "revoke":
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(204)
	default:
		writeJSON(w, 201, agentKeyCreatedJSON{agentKeyJSON: keyJSON(rec), Token: rec.Token})
	}
}

// All operations share the same live authorization and tenant fence. No general
// agent/key creation authority is delegated. All record writes precede notice
// acceptance, and the workspace audit is the last append in this transaction.
func (m *Module) messagingAssistant(ctx context.Context, p tenant.Principal, operation, id string, in assistantInput) (keyRecord, error) {
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 15*time.Second)
	defer cancel()
	var rec keyRecord
	if operation != "create" && operation != "rotate" && operation != "revoke" {
		return rec, errAssistantInput
	}
	in.Name = strings.TrimSpace(in.Name)
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		owner, err := authz.RequireMessagingAssistantTx(ctx, tx, p)
		if err != nil {
			return err
		}
		now := time.Now()
		if m.assistantNow != nil {
			now = m.assistantNow()
		}
		if operation != "revoke" && (in.ExpiresAt == nil || !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(30*24*time.Hour))) {
			return errAssistantInput
		}
		if operation == "create" {
			if in.Name == "" || utf8.RuneCountInString(in.Name) > 200 || strings.ContainsAny(in.Name, "\x00\r\n") {
				return errAssistantInput
			}
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE lower(name)=lower($1))`, in.Name).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return errAssistantName
			}
		} else {
			var status string
			err := tx.QueryRow(ctx, `SELECT name,status FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid
			 AND kind='agent' AND messaging_only IS TRUE AND messaging_owner_id=$3::uuid FOR NO KEY UPDATE`, p.TenantID, id, owner).Scan(&in.Name, &status)
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			if err != nil {
				return err
			}
			if operation == "rotate" && status != "active" {
				return errAgentDeactivated
			}
		}
		if operation != "revoke" {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT p.id FROM principals p
			 WHERE p.messaging_only IS TRUE AND p.messaging_owner_id=$1::uuid AND p.status='active'
			 AND p.id IS DISTINCT FROM NULLIF($2,'')::uuid AND EXISTS(SELECT 1 FROM agent_keys k WHERE k.principal_id=p.id
			 AND k.revoked_at IS NULL AND k.expires_at>$3) LIMIT 3) active`, owner, id, now).Scan(&count); err != nil {
				return err
			}
			if count >= 3 {
				return errAssistantLimit
			}
		}
		if m.AssistantNoticeTx == nil {
			return errors.New("assistant notice path unavailable")
		}
		if operation == "create" {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles,agent_access_configured,messaging_only,messaging_owner_id)
			 VALUES($1::uuid,'agent',$2,'{}',true,true,$3::uuid) RETURNING id::text`, p.TenantID, in.Name, owner).Scan(&id); err != nil {
				return err
			}
		} else {
			// Immediate revocation is within the allowed maximum 24-hour overlap.
			if _, err := tx.Exec(ctx, `UPDATE agent_keys SET revoked_at=$2 WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id, now); err != nil {
				return err
			}
		}
		if operation == "revoke" {
			if _, err := tx.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1::uuid`, id); err != nil {
				return err
			}
			rec.PrincipalID = id
		} else {
			prefix, err := prefixFor(p.TenantID)
			if err != nil {
				return err
			}
			secret, hash, err := newSecret()
			if err != nil {
				return err
			}
			rec = keyRecord{PrincipalID: id, Name: in.Name, Prefix: prefix, Scopes: authz.MessagingAssistantScopes(), ExpiresAt: in.ExpiresAt, CreatedByPrincipalID: &owner}
			if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,expires_at,created_by_principal_id,person_owner_required,full_access)
			 VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8::uuid,true,false) RETURNING id::text,created_at`, p.TenantID, id, in.Name, prefix, hash, rec.Scopes, in.ExpiresAt, owner).Scan(&rec.ID, &rec.CreatedAt); err != nil {
				return err
			}
			rec.Token = "aeon_" + prefix + "_" + secret
		}
		action := map[string]string{"create": "provisioned", "rotate": "rotated", "revoke": "revoked"}[operation]
		// The audit actor is another existing FK parent. Fence it before the
		// notice appends its first event, along with the owner and assistant.
		if _, err := tx.Exec(ctx, `SELECT id FROM principals WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, []string{p.ID, owner, id}); err != nil {
			return err
		}
		var noticeID string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&noticeID); err != nil {
			return err
		}
		body := fmt.Sprintf("Messaging assistant %s (%s) %s by %s for %s. No action needed.", in.Name, id, action, p.ID, owner)
		if err := m.AssistantNoticeTx(ctx, tx, p.TenantID, owner, noticeID, body); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "messaging_assistant." + action, After: map[string]any{
			"principal_id": id, "caller_id": p.ID, "owner_id": owner, "key_id": rec.ID,
			"scopes": authz.MessagingAssistantScopes(), "expires_at": in.ExpiresAt,
			"summary": fmt.Sprintf("messaging assistant %s by %s for %s", action, p.ID, owner),
		}})
		return err
	})
	if err != nil {
		return keyRecord{}, err
	}
	return rec, nil
}

// General creation/rotation and scope/expiry edits cannot bypass the dedicated
// lifecycle. Revocation through the existing owner-facing controls stays valid.
func refuseMessagingAssistantKeyTx(ctx context.Context, tx pgx.Tx, principalID string) error {
	var marked bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(messaging_only,false) FROM principals WHERE id=$1::uuid`, principalID).Scan(&marked); err != nil {
		return err
	}
	if marked {
		return authz.ErrForbidden
	}
	return nil
}
