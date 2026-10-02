// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const workstationBodyLimit = 2 << 20

type workstationDigestKey struct{}
type workstationSignerKey struct{}

type workstationChallenge struct {
	Code    string    `json:"code"`
	ID      string    `json:"challenge_id"`
	Nonce   string    `json:"nonce"`
	Digest  string    `json:"action_digest"`
	Summary string    `json:"summary"`
	Expires time.Time `json:"expires_at"`
}

func workstationGovernance(r *http.Request) bool {
	permission := authz.RoutePermissions[r.Pattern]
	if authz.OwnerWorkstationPermission(permission) {
		return true
	}
	// Read-only catalogs are governance calls too, despite their ordinary scopes.
	return permission == "members.read" || permission == "roles.read" || permission == "settings.read"
}

func workstationStepUp(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	switch authz.RoutePermissions[r.Pattern] {
	case "rules.publish", "keys.manage", "roles.manage", "settings.manage", "approvals.decide", "approvals.decide_high":
		return true
	case "members.manage":
		return r.Method == http.MethodDelete || strings.HasSuffix(r.URL.Path, "/deactivate") || strings.HasSuffix(r.URL.Path, "/workspace-role") || strings.Contains(r.URL.Path, "/aliases")
	}
	return false
}

func workstationAction(p tenant.Principal, r *http.Request, body []byte) (string, string) {
	bodyDigest := sha256.Sum256(body)
	// Length-unambiguous, versioned encoding includes exact URI/query and headers
	// that affect concurrency or representation. No submitted text is retained.
	input, _ := json.Marshal([]string{"aeon.owner-workstation.v1", p.TenantID, p.KeyID, p.ID, p.WorkstationComputerID,
		r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), r.Header.Get("If-Match"), r.Header.Get("If-None-Match"), hex.EncodeToString(bodyDigest[:])})
	digest := sha256.Sum256(input)
	summary := r.Method + " " + r.URL.Path
	return hex.EncodeToString(digest[:]), summary
}

func verifyWorkstationSignature(public, nonce, digest, signature string) bool {
	key := attachwatch.LocalAuthPublicKey(public)
	if key == nil || !attachwatch.LocalAuthNonceValid(nonce) || !attachwatch.LocalAuthNonceValid(digest) || len(signature) > 144 || strings.ContainsAny(signature, "\r\n") {
		return false
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(signature)
	hash := sha256.Sum256([]byte(nonce + digest))
	return err == nil && ecdsa.VerifyASN1(key, hash[:], raw)
}

// This fence runs at every handler transaction, including the final write after
// external I/O. It takes the tenant first and rechecks live roles/scopes/marking;
// no transaction trusts the earlier middleware authorization snapshot.
// Pairing precedes the tree (as in run dispatch); both precede record locks.
func workstationGuard(p tenant.Principal, permission string, scope authz.Scope) db.TenantGuard {
	return func(ctx context.Context, tx pgx.Tx, tid string) error {
		if tid != p.TenantID {
			return authz.ErrForbidden
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','3s',true),set_config('statement_timeout','15s',true)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, tid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-pairing:'||$1::text,0))`, tid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, tid); err != nil {
			return err
		}
		// Pairing disconnect fences on the computer before revoking its keys.
		if _, err := tx.Exec(ctx, `SELECT id FROM agent_pairing_computers WHERE tenant_id=$1::uuid AND id=$2::uuid FOR SHARE`, tid, p.WorkstationComputerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT id FROM agent_keys WHERE tenant_id=$1::uuid AND id=$2::uuid FOR SHARE`, tid, p.KeyID); err != nil {
			return err
		}
		public, _, err := authz.WorkstationKeyTx(ctx, tx, p)
		if err != nil {
			return err
		}
		if approved, ok := ctx.Value(workstationSignerKey{}).(string); ok && public != approved {
			return authz.ErrForbidden
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.owner_workstation_key',$1,true)`, p.KeyID); err != nil {
			return err
		}
		if _, known := authz.Lookup(permission); known {
			return authz.RequireTx(ctx, tx, p, permission, scope)
		}
		return nil
	}
}

func (m *Module) auditWorkstation(r *http.Request, p tenant.Principal, step bool, outcome string) error {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	digest, _ := ctx.Value(workstationDigestKey{}).(string)
	return m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, p.TenantID); err != nil {
			return err
		}
		_, err := events.Append(ctx, tx, p, events.Change{Type: "agent_key.governance_used", After: map[string]any{
			"key_id": p.KeyID, "principal_id": p.ID, "computer_id": p.WorkstationComputerID,
			"action": r.Pattern, "step_up": step, "outcome": outcome,
			"route": r.URL.EscapedPath(), "action_digest": digest,
		}})
		return err
	})
}

func (m *Module) serveWorkstation(w http.ResponseWriter, r *http.Request, p tenant.Principal, next http.Handler) {
	w.Header().Set("Cache-Control", "no-store")
	permission := authz.RoutePermissions[r.Pattern]
	governance := workstationGovernance(r)
	step := false
	if governance && workstationStepUp(r) {
		if len(r.URL.RequestURI()) > 2048 || len(r.URL.Path) > 500 {
			if err := m.auditWorkstation(r, p, false, "invalid_action"); err != nil {
				writeInternal(w)
				return
			}
			writeBadRequest(w, "action URI too long")
			return
		}
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(15 * time.Second))
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, workstationBodyLimit))
		_ = controller.SetReadDeadline(time.Time{})
		if err != nil {
			if err := m.auditWorkstation(r, p, false, "invalid_body"); err != nil {
				writeInternal(w)
				return
			}
			writeJSON(w, http.StatusRequestEntityTooLarge, errorJSON{Error: "action body exceeds 2 MiB"})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		digest, summary := workstationAction(p, r, body)
		r = r.WithContext(context.WithValue(r.Context(), workstationDigestKey{}, digest))
		var challenge workstationChallenge
		var approvedSigner string
		proof := r.Header.Get("Aeon-Step-Up")
		err = m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
			if err := workstationGuard(p, permission, authz.RouteScope(r.Context()))(r.Context(), tx, p.TenantID); err != nil {
				return err
			}
			public, _, err := authz.WorkstationKeyTx(r.Context(), tx, p)
			if err != nil {
				return err
			}
			if proof == "" {
				var nonce [32]byte
				if _, err := rand.Read(nonce[:]); err != nil {
					return err
				}
				challenge = workstationChallenge{Code: "step_up_required", Nonce: hex.EncodeToString(nonce[:]), Digest: digest, Summary: summary}
				return tx.QueryRow(r.Context(), `INSERT INTO owner_workstation_challenges(tenant_id,key_id,principal_id,computer_id,nonce,action_digest,public_key,expires_at)
				 VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp()+interval '2 minutes')
				 ON CONFLICT(tenant_id,key_id) DO UPDATE SET id=gen_random_uuid(),nonce=EXCLUDED.nonce,action_digest=EXCLUDED.action_digest,public_key=EXCLUDED.public_key,expires_at=EXCLUDED.expires_at
				 RETURNING id::text,expires_at`, p.TenantID, p.KeyID, p.ID, p.WorkstationComputerID, challenge.Nonce, digest, public).Scan(&challenge.ID, &challenge.Expires)
			}
			id, signature, ok := strings.Cut(proof, ".")
			if !ok || len(proof) > 181 || !uuidRe.MatchString(id) || len(r.Header.Values("Aeon-Step-Up")) != 1 {
				return authz.ErrForbidden
			}
			var nonce, stored, pinned string
			var fresh bool
			err = tx.QueryRow(r.Context(), `SELECT nonce,action_digest,public_key,expires_at>clock_timestamp() FROM owner_workstation_challenges
			 WHERE tenant_id=$1 AND id=$2 AND key_id=$3 AND principal_id=$4 AND computer_id=$5 FOR UPDATE`, p.TenantID, id, p.KeyID, p.ID, p.WorkstationComputerID).Scan(&nonce, &stored, &pinned, &fresh)
			if errors.Is(err, pgx.ErrNoRows) {
				return authz.ErrForbidden
			}
			if err != nil {
				return err
			}
			if !fresh || stored != digest || pinned != public || !verifyWorkstationSignature(public, nonce, digest, signature) {
				return authz.ErrForbidden
			}
			_, err = tx.Exec(r.Context(), `DELETE FROM owner_workstation_challenges WHERE tenant_id=$1 AND id=$2`, p.TenantID, id)
			approvedSigner = public
			return err
		})
		outcome := "admitted"
		if err != nil {
			outcome = "proof_rejected"
		} else if proof == "" {
			outcome = "step_up_required"
		} else {
			step = true
			r = r.WithContext(context.WithValue(r.Context(), workstationSignerKey{}, approvedSigner))
		}
		if auditErr := m.auditWorkstation(r, p, step, outcome); auditErr != nil {
			writeInternal(w)
			return
		}
		if errors.Is(err, authz.ErrForbidden) {
			writeForbidden(w)
			return
		}
		if err != nil {
			writeInternal(w)
			return
		}
		if proof == "" {
			writeJSON(w, http.StatusPreconditionRequired, challenge)
			return
		}
	} else if governance {
		if err := m.auditWorkstation(r, p, false, "admitted"); err != nil {
			writeInternal(w)
			return
		}
	}
	ctx := db.WithTenantGuard(r.Context(), workstationGuard(p, permission, authz.RouteScope(r.Context())))
	next.ServeHTTP(w, r.WithContext(ctx))
}

func (m *Module) handleOwnerWorkstation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Person || r.Header.Get("Authorization") != "" {
		writeForbidden(w)
		return
	}
	origin := strings.TrimRight(m.cfg.PublicURL, "/")
	if origin == "" {
		origin = "http://" + r.Host
		if r.TLS != nil {
			origin = "https://" + r.Host
		}
	}
	if r.Header.Get("Origin") != origin {
		writeForbidden(w)
		return
	}
	id := r.PathValue("id")
	var body struct {
		Marked   *bool  `json:"owner_workstation"`
		Computer string `json:"workstation_computer_id"`
	}
	if !uuidRe.MatchString(id) {
		writeBadRequest(w, "invalid id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
		writeBadRequest(w, "invalid request")
		return
	}
	if body.Marked == nil || *body.Marked && !uuidRe.MatchString(body.Computer) || !*body.Marked && body.Computer != "" {
		writeBadRequest(w, "marking requires a computer; unmarking omits it")
		return
	}
	var out keyRecord
	err := m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, p.TenantID); err != nil {
			return err
		}
		effective, err := authz.EffectiveTx(r.Context(), tx, p, "")
		if err != nil {
			return err
		}
		if effective.Workspace.Role == nil || effective.Workspace.Role.Key != "owner" {
			return authz.ErrForbidden
		}
		key, err := lockAgentKey(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if *body.Marked && (key.RevokedAt != nil || key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now())) {
			return errKeyInactive
		}
		if *body.Marked {
			var public string
			err = tx.QueryRow(r.Context(), `SELECT c.local_auth_public_key FROM agent_pairing_computers c
			 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
			 JOIN principals a ON a.tenant_id=c.tenant_id AND a.id=c.principal_id
			 WHERE c.tenant_id=$1 AND c.id=$2 AND c.principal_id=$3 AND c.state='connected' AND q.state='redeemed' AND a.status='active' AND a.kind='agent'`, p.TenantID, body.Computer, key.PrincipalID).Scan(&public)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && attachwatch.LocalAuthPublicKey(public) == nil {
				return authz.ErrForbidden
			}
			if err != nil {
				return err
			}
		}
		before := keySnapshot(key)
		key.OwnerWorkstation = *body.Marked
		key.WorkstationComputerID = nil
		if *body.Marked {
			key.WorkstationComputerID = &body.Computer
		} else {
			key.Scopes = slices.DeleteFunc(key.Scopes, func(scope string) bool { return authz.OwnerWorkstationPermission(strings.ReplaceAll(scope, ":", ".")) })
		}
		if _, err = tx.Exec(r.Context(), `UPDATE agent_keys SET owner_workstation=$2,workstation_computer_id=$3,scopes=$4,workstation_generation=workstation_generation+1 WHERE id=$1`, id, key.OwnerWorkstation, key.WorkstationComputerID, key.Scopes); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `DELETE FROM owner_workstation_challenges WHERE key_id=$1`, id); err != nil {
			return err
		}
		_, err = events.Append(r.Context(), tx, p, events.Change{Type: "agent_key.owner_workstation_changed", Before: before, After: keySnapshot(key)})
		out = key
		return err
	})
	switch {
	case errors.Is(err, authz.ErrForbidden):
		writeForbidden(w)
	case errors.Is(err, errNotFound):
		writeJSON(w, 404, errorJSON{Error: "key not found"})
	case errors.Is(err, errKeyInactive), isUnique(err):
		writeJSON(w, 409, errorJSON{Error: "key inactive or another marked key exists"})
	case err != nil:
		writeInternal(w)
	default:
		writeJSON(w, 200, keyJSON(out))
	}
}
