// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/operatoractor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

var (
	errNotMember        = errors.New("not a member")
	errNotFound         = errors.New("not found")
	errNotAgent         = errors.New("not an agent")
	errServicePrincipal = errors.New("agent keys cannot be issued for service principals")
)

func scanPrincipal(row pgx.Row) (tenant.Principal, error) {
	var p tenant.Principal
	var kind string
	var roles pgtype.FlatArray[string]
	if err := row.Scan(&p.ID, &p.TenantID, &kind, &p.Name, &roles); err != nil {
		return tenant.Principal{}, err
	}
	p.Kind = tenant.PrincipalKind(kind)
	p.Roles = []string(roles)
	if p.Roles == nil {
		p.Roles = []string{}
	}
	return p, nil
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func (m *Module) tenantBySlug(ctx context.Context, slug string) (string, error) {
	return tenantbootstrap.ResolveSlug(ctx, m.pool, slug)
}

// resolveOIDCPerson resolves issuer+subject only within the signed target
// tenant. A bootstrap email may create the original tenant admin, but cannot
// enroll itself in any additional tenant.
func (m *Module) resolveOIDCPerson(ctx context.Context, tenantID, slug, issuer, subject, email, name string, emailVerified bool, inviteToken string) (tenant.Principal, string, error) {
	// Sign-in is system code, not a caller's read: accepting an invite checks
	// that each invited project still exists, so it sees every project
	// (ADR-003 P2). Nothing read here is returned to the browser.
	ctx = db.AllProjects(ctx, "sign-in and invite acceptance")
	var p tenant.Principal
	var identityID string
	err := m.inTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		var emailArg, displayArg any
		if strings.TrimSpace(email) != "" {
			emailArg = strings.TrimSpace(email)
		}
		if strings.TrimSpace(name) != "" {
			displayArg = strings.TrimSpace(name)
		}
		var oldEmail, oldDisplay *string
		var hadIdentity bool
		err := tx.QueryRow(ctx, `SELECT email,display_name FROM identities WHERE issuer=$1 AND subject=$2`, issuer, subject).Scan(&oldEmail, &oldDisplay)
		if err == nil {
			hadIdentity = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email,display_name)
			VALUES($1,$2,$3,$4) ON CONFLICT(issuer,subject) DO UPDATE
			SET email=COALESCE(EXCLUDED.email,identities.email),
			    display_name=COALESCE(EXCLUDED.display_name,identities.display_name)
			RETURNING id::text`, issuer, subject, emailArg, displayArg).Scan(&identityID); err != nil {
			return err
		}
		p, err = scanPrincipal(tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,kind,name,roles
			FROM principals WHERE tenant_id=$1::uuid AND identity_id=$2::uuid AND kind='person'`, tenantID, identityID))
		if err == nil {
			var newEmail, newDisplay *string
			if err := tx.QueryRow(ctx, `SELECT email,display_name FROM identities WHERE id=$1::uuid`, identityID).Scan(&newEmail, &newDisplay); err != nil {
				return err
			}
			if hadIdentity && (!sameNullable(oldEmail, newEmail) || !sameNullable(oldDisplay, newDisplay)) {
				_, err = events.Append(ctx, tx, p, events.Change{Type: "identity.updated",
					Before: map[string]any{"identity_id": identityID, "email": oldEmail, "display_name": oldDisplay},
					After:  map[string]any{"identity_id": identityID, "email": newEmail, "display_name": newDisplay}})
				return err
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if slug != m.cfg.BootstrapTenantSlug || !adminEmail(email, m.cfg.BootstrapAdminEmail) {
			// An invite enrolls only a verified email. The token may select which
			// invite, but it never substitutes for that address.
			if emailVerified {
				invited, accErr := authz.AcceptInvite(ctx, tx, tenantID, identityID, email, name, inviteToken)
				if accErr == nil {
					p = invited
					return nil
				}
				if !errors.Is(accErr, authz.ErrNoInvite) {
					return accErr
				}
			}
			return errNotMember
		}
		p, err = scanPrincipal(tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,identity_id,name,roles)
			VALUES($1::uuid,'person',$2::uuid,$3,ARRAY['admin'])
			ON CONFLICT(tenant_id,identity_id) WHERE identity_id IS NOT NULL
			DO NOTHING
			RETURNING id::text,tenant_id::text,kind,name,roles`, tenantID, identityID, name))
		if errors.Is(err, pgx.ErrNoRows) {
			p, err = scanPrincipal(tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,kind,name,roles
				FROM principals WHERE tenant_id=$1::uuid AND identity_id=$2::uuid AND kind='person'`, tenantID, identityID))
			return err
		}
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "tenant.principal_bound",
			After: map[string]any{"principal_id": p.ID, "issuer": issuer, "subject": subject, "name": p.Name, "roles": p.Roles}})
		return err
	})
	return p, identityID, err
}

func sameNullable(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (m *Module) upsertIdentity(ctx context.Context, tenantID, issuer, subject, email, display string) (string, error) {
	var emailArg, displayArg any
	if strings.TrimSpace(email) != "" {
		emailArg = strings.TrimSpace(email)
	}
	if strings.TrimSpace(display) != "" {
		displayArg = strings.TrimSpace(display)
	}
	var id string
	err := m.inTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO identities (issuer, subject, email, display_name)
			VALUES ($1, $2, $3, $4) ON CONFLICT (issuer, subject) DO NOTHING`, issuer, subject, emailArg, displayArg); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM identities WHERE issuer=$1 AND subject=$2`, issuer, subject).Scan(&id)
	})
	return id, err
}

// ensurePerson returns the person principal for this identity inside the tenant.
// The bootstrap admin email is provisioned with the admin role on first sign-in.
func (m *Module) ensurePerson(ctx context.Context, tenantID, identityID, email, name string) (tenant.Principal, error) {
	var p tenant.Principal
	err := m.inTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		p, err = scanPrincipal(tx.QueryRow(ctx, `
			SELECT id::text, tenant_id::text, kind, name, roles
			FROM principals
			WHERE identity_id = $1::uuid AND kind = 'person'
		`, identityID))
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !adminEmail(email, m.cfg.BootstrapAdminEmail) {
			return errNotMember
		}
		if strings.TrimSpace(name) == "" {
			name = strings.TrimSpace(email)
		}
		if _, err := tx.Exec(ctx, "SAVEPOINT person_insert"); err != nil {
			return err
		}
		p, err = scanPrincipal(tx.QueryRow(ctx, `
			INSERT INTO principals (tenant_id, kind, identity_id, name, roles)
			VALUES ($1::uuid, 'person', $2::uuid, $3, $4)
			RETURNING id::text, tenant_id::text, kind, name, roles
		`, tenantID, identityID, name, []string{"admin"}))
		if isUnique(err) {
			if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT person_insert"); rbErr != nil {
				return rbErr
			}
			p, err = scanPrincipal(tx.QueryRow(ctx, `
				SELECT id::text, tenant_id::text, kind, name, roles
				FROM principals
				WHERE identity_id = $1::uuid AND kind = 'person'
			`, identityID))
			return err
		}
		if err != nil {
			return err
		}
		if _, err := events.Append(ctx, tx, p, events.Change{Type: "tenant.principal_bound",
			After: map[string]any{"principal_id": p.ID, "name": p.Name, "roles": p.Roles}}); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "RELEASE SAVEPOINT person_insert")
		return err
	})
	return p, err
}

func adminEmail(got, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(got), want)
}

// personByEmail resolves dev-login email to its canonical person in the tenant.
// Prefer link targets over older imported identities with the same email.
func (m *Module) personByEmail(ctx context.Context, tenantID, email string) (tenant.Principal, string, error) {
	var p tenant.Principal
	var identityID string
	err := m.inTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		var kind string
		var roles pgtype.FlatArray[string]
		err := tx.QueryRow(ctx, `
			SELECT canonical.id::text, canonical.tenant_id::text, canonical.kind,
			       canonical.name, canonical.roles, COALESCE(canonical.identity_id, p.identity_id)::text
			FROM principals p
			LEFT JOIN identities i ON i.id = p.identity_id
			JOIN principals canonical ON canonical.tenant_id=p.tenant_id
			    AND canonical.id=COALESCE(p.linked_to,p.id)
			WHERE p.tenant_id=$2::uuid AND p.kind='person'
			    AND lower(COALESCE(NULLIF(p.email,''),i.email))=lower($1)
			    AND COALESCE(canonical.identity_id,p.identity_id) IS NOT NULL
			ORDER BY (EXISTS (SELECT 1 FROM principals source
			    WHERE source.tenant_id=p.tenant_id AND source.linked_to=canonical.id)) DESC,
			    (p.linked_to IS NULL) DESC, canonical.created_at, canonical.id, p.id
			LIMIT 1
		`, email, tenantID).Scan(&p.ID, &p.TenantID, &kind, &p.Name, &roles, &identityID)
		if err != nil {
			return err
		}
		p.Kind = tenant.PrincipalKind(kind)
		p.Roles = []string(roles)
		if p.Roles == nil {
			p.Roles = []string{}
		}
		return nil
	})
	return p, identityID, err
}

func (m *Module) startSession(ctx context.Context, identityID, tenantID, principalID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	err := m.inTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO sessions (id, identity_id, tenant_id, principal_id, expires_at)
			SELECT $1,$2::uuid,$3::uuid,p.id,now() + interval '30 days'
			FROM principals p WHERE p.tenant_id=$3::uuid AND p.id=$4::uuid AND p.status='active'
			RETURNING id`, sessionID(raw), identityID, tenantID, principalID).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_principal($1::uuid,$2::uuid)`, tenantID, principalID)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func decodeSessionToken(value string) ([]byte, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return nil, errBadCookie
	}
	return raw, nil
}

func (m *Module) authenticateSession(ctx context.Context, raw []byte) (tenant.Principal, bool, error) {
	id := sessionID(raw)
	var tenantID, principalID string
	err := m.inTenant(ctx, m.pool, tenantbootstrap.LookupTenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.session_id',$1,true)`, id); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `UPDATE sessions
			SET last_seen_at = now(), expires_at = now() + interval '30 days'
			WHERE id = $1 AND expires_at > now()
			RETURNING tenant_id::text, principal_id::text`, id).Scan(&tenantID, &principalID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		_ = m.deleteSession(ctx, raw)
		return tenant.Principal{}, false, nil
	}
	if err != nil {
		return tenant.Principal{}, false, err
	}
	var p tenant.Principal
	err = m.inTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		var scanErr error
		p, scanErr = displayPrincipal(ctx, tx, tenantID, principalID)
		return scanErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		_ = m.deleteSession(ctx, raw)
		return tenant.Principal{}, false, nil
	}
	if err != nil {
		return tenant.Principal{}, false, err
	}
	return p, true, nil
}

func (m *Module) deleteSession(ctx context.Context, raw []byte) error {
	return m.inTenant(ctx, m.pool, tenantbootstrap.LookupTenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.session_id',$1,true)`, sessionID(raw)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID(raw))
		return err
	})
}

// prefixFor builds aeon_<prefix>_<secret>'s prefix: 32 hex chars of the tenant
// uuid, then 16 hex chars of randomness. The tenant half lets the bearer
// resolver enter InTenant before it can see the row.
func prefixFor(tenantID string) (string, error) {
	compact := strings.ToLower(strings.ReplaceAll(tenantID, "-", ""))
	if len(compact) != 32 || !isHex(compact) {
		return "", fmt.Errorf("tenant id %q is not a uuid", tenantID)
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return compact + hex.EncodeToString(buf), nil
}

func tenantFromPrefix(prefix string) (string, bool) {
	if len(prefix) < 48 || !isHex(prefix[:32]) {
		return "", false
	}
	h := strings.ToLower(prefix[:32])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], true
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return s != ""
}

func newSecret() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	secret := hex.EncodeToString(buf)
	return secret, hashSecret(secret), nil
}

func (m *Module) authenticateAgent(ctx context.Context, prefix, secret string) (tenant.Principal, bool, error) {
	tenantID, ok := tenantFromPrefix(prefix)
	if !ok {
		return tenant.Principal{}, false, nil
	}
	var p tenant.Principal
	err := m.inTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		var principalID, gotTenant string
		var creatorID *string
		var scopes pgtype.FlatArray[string]
		err := tx.QueryRow(ctx, `
			UPDATE agent_keys k
			SET last_used_at = now()
			WHERE k.prefix = $1 AND k.hash = $2
			  AND k.revoked_at IS NULL
     AND NOT EXISTS(SELECT 1 FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
      WHERE c.principal_id=k.principal_id AND (c.state='revoked' OR q.state<>'redeemed'))
			  AND (k.expires_at IS NULL OR k.expires_at > now())
			  AND EXISTS (
			    SELECT 1 FROM principals p
			    WHERE p.id = k.principal_id AND p.kind = 'agent' AND p.status='active'
			      AND NOT (p.roles && ARRAY['system','importer','operator','embedding','quote_public_service','quote_confirmation_service','portal_public_service']::text[])
			  )
			RETURNING k.principal_id::text, k.tenant_id::text, k.scopes, k.created_by_principal_id::text
		`, prefix, hashSecret(secret)).Scan(&principalID, &gotTenant, &scopes, &creatorID)
		if err != nil {
			return err
		}
		if !strings.EqualFold(gotTenant, tenantID) {
			return pgx.ErrNoRows
		}
		p, err = scanPrincipal(tx.QueryRow(ctx, `
			SELECT id::text, tenant_id::text, kind, name, roles
			FROM principals WHERE id = $1::uuid
		`, principalID))
		p.Scopes = []string(scopes)
		if creatorID != nil {
			p.KeyCreatorID = *creatorID
		}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return tenant.Principal{}, false, nil
	}
	if err != nil {
		return tenant.Principal{}, false, err
	}
	if p.Kind != tenant.Agent {
		return tenant.Principal{}, false, nil
	}
	return p, true, nil
}

type keyRecord struct {
	ID          string
	PrincipalID string
	Name        string
	Prefix      string
	Scopes      []string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
	Token       string
}

func (m *Module) createAgentKey(ctx context.Context, p tenant.Principal, name, principalID string, scopes []string, expires *time.Time) (keyRecord, error) {
	var rec keyRecord
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		rec, err = m.createAgentKeyTx(ctx, tx, p, name, principalID, scopes, expires)
		return err
	})
	return rec, err
}

// createAgentKeyTx is shared by creation and atomic rotation; all grant and
// creator-ceiling checks remain on this path. The caller owns db.InTenant.
func (m *Module) createAgentKeyTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, name, principalID string, scopes []string, expires *time.Time) (keyRecord, error) {
	if scopes == nil {
		scopes = []string{}
	}
	var rec keyRecord
	err := func() error {
		actorID := p.ID
		if actorID == "" {
			var err error
			actorID, err = operatoractor.Ensure(ctx, tx, p.TenantID)
			if err != nil {
				return err
			}
		} else {
			var tenantLock string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR UPDATE`, p.TenantID).Scan(&tenantLock); err != nil {
				return err
			}
			if p.Kind != tenant.Person {
				return authz.ErrForbidden
			}
			if err := authz.RequireTx(ctx, tx, p, "keys.manage", authz.Scope{}); err != nil {
				return err
			}
		}
		var err error
		if principalID != "" {
			var kind, agentName string
			var reserved bool
			err = tx.QueryRow(ctx, `SELECT kind,name,roles && ARRAY['system','importer','operator','embedding','quote_public_service','quote_confirmation_service','portal_public_service']::text[]
				FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid FOR UPDATE`, p.TenantID, principalID).Scan(&kind, &agentName, &reserved)
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			if err != nil {
				return err
			}
			if kind != string(tenant.Agent) {
				return errNotAgent
			}
			if reserved {
				return errServicePrincipal
			}
			if name == "" {
				name = agentName
			}
		} else {
			rows, err := tx.Query(ctx, `
			SELECT id::text,kind,roles && ARRAY['system','importer','operator','embedding','quote_public_service','quote_confirmation_service','portal_public_service']::text[]
			FROM principals WHERE name=$1 ORDER BY created_at,id FOR UPDATE`, name)
			if err != nil {
				return err
			}
			service := false
			for rows.Next() {
				var id, kind string
				var reserved bool
				if err := rows.Scan(&id, &kind, &reserved); err != nil {
					rows.Close()
					return err
				}
				service = service || reserved
				if kind == string(tenant.Agent) && principalID == "" {
					principalID = id
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if service {
				return errServicePrincipal
			}
			if principalID == "" {
				err = tx.QueryRow(ctx, `
				INSERT INTO principals (tenant_id, kind, name, roles)
				VALUES ($1::uuid, 'agent', $2, '{}')
				RETURNING id::text
			`, p.TenantID, name).Scan(&principalID)
			}
			if err != nil {
				return err
			}
		}
		if err := pairedIdentity(ctx, tx, principalID); err != nil {
			return err
		}
		if err := ensureAgentBinding(ctx, tx, p, actorID, principalID, name, scopes); err != nil {
			return err
		}
		for attempt := 0; attempt < 5; attempt++ {
			prefix, err := prefixFor(p.TenantID)
			if err != nil {
				return err
			}
			secret, hash, err := newSecret()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "SAVEPOINT key_insert"); err != nil {
				return err
			}
			var id string
			var created time.Time
			// This column caps HTTP-created keys by the creator's live
			// permissions. Operator keys have no human creator ceiling.
			var creator any
			if p.ID != "" {
				creator = p.ID
			}
			err = tx.QueryRow(ctx, `
				INSERT INTO agent_keys (tenant_id, principal_id, name, prefix, hash, scopes, expires_at, created_by_principal_id)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8::uuid)
				RETURNING id::text, created_at
			`, p.TenantID, principalID, name, prefix, hash, scopes, expires, creator).Scan(&id, &created)
			if isUnique(err) {
				if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT key_insert"); rbErr != nil {
					return rbErr
				}
				continue
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT key_insert"); err != nil {
				return err
			}
			rec = keyRecord{
				ID:          id,
				PrincipalID: principalID,
				Name:        name,
				Prefix:      prefix,
				Scopes:      scopes,
				CreatedAt:   created,
				ExpiresAt:   expires,
				Token:       "aeon_" + prefix + "_" + secret,
			}
			_, err = events.Append(ctx, tx, tenant.Principal{ID: actorID, TenantID: p.TenantID}, events.Change{
				Type: "agent_key.created", After: keySnapshot(rec),
			})
			if err != nil {
				return err
			}
			return nil
		}
		return errors.New("agent key prefix collision")
	}()
	return rec, err
}

func (m *Module) grantJourneyScopes(ctx context.Context, tenantID, keyID, principalID string, scopes []string) error {
	return m.inTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		actorID, err := operatoractor.Ensure(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		var before []string
		var actualPrincipalID string
		var expectedPrincipal any
		if principalID != "" {
			expectedPrincipal = principalID
		}
		if err := tx.QueryRow(ctx, `SELECT principal_id::text,scopes FROM agent_keys WHERE tenant_id=$1::uuid AND id=$2::uuid AND ($3::uuid IS NULL OR principal_id=$3::uuid) AND revoked_at IS NULL FOR UPDATE`, tenantID, keyID, expectedPrincipal).Scan(&actualPrincipalID, &before); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			return err
		}
		after := slices.Clone(before)
		for _, scope := range scopes {
			if !slices.Contains(after, scope) {
				after = append(after, scope)
			}
		}
		if slices.Equal(before, after) {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_keys SET scopes=$3::text[] WHERE id=$1::uuid AND principal_id=$2::uuid`, keyID, actualPrincipalID, after); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, tenant.Principal{ID: actorID, TenantID: tenantID}, events.Change{
			Type:   "agent_key.scopes_extended",
			Before: map[string]any{"key_id": keyID, "principal_id": actualPrincipalID, "scopes": before},
			After:  map[string]any{"key_id": keyID, "principal_id": actualPrincipalID, "scopes": after},
		})
		return err
	})
}

func ensureAgentBinding(ctx context.Context, tx pgx.Tx, creator tenant.Principal, actorID, agentID, name string, scopes []string) error {
	requested := map[string]bool{}
	for _, scope := range scopes {
		key := strings.ReplaceAll(scope, ":", ".")
		if _, ok := authz.Lookup(key); !ok {
			return authz.ErrForbidden
		}
		requested[key] = true
	}
	if creator.ID != "" {
		effective, err := authz.EffectiveTx(ctx, tx, creator, "")
		if err != nil {
			return err
		}
		for key := range requested {
			if !slices.Contains(effective.Workspace.Permissions, key) {
				return authz.ErrForbidden
			}
		}
	}
	var configured bool
	if err := tx.QueryRow(ctx, `SELECT agent_access_configured FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid`, creator.TenantID, agentID).Scan(&configured); err != nil {
		return err
	}
	if configured {
		ceiling, err := authz.AgentKeyCeilingTx(ctx, tx, tenant.Principal{ID: agentID, TenantID: creator.TenantID, Kind: tenant.Agent})
		if err != nil {
			return err
		}
		for key := range requested {
			if !slices.Contains(ceiling, key) {
				return authz.ErrForbidden
			}
		}
		return nil
	}
	var roleID, roleKey string
	var builtin bool
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.key,r.builtin FROM role_bindings b
		JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
		WHERE b.tenant_id=$1::uuid AND b.principal_id=$2::uuid AND b.scope_type='workspace' FOR UPDATE OF b`, creator.TenantID, agentID).Scan(&roleID, &roleKey, &builtin)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	actor := tenant.Principal{ID: actorID, TenantID: creator.TenantID}
	if errors.Is(err, pgx.ErrNoRows) {
		roleKey = "agent_" + strings.ReplaceAll(agentID, "-", "")
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name,description)
			VALUES($1::uuid,$2,$3,'Permissions assigned to this agent') RETURNING id::text`, creator.TenantID, roleKey, "Agent "+name).Scan(&roleID); err != nil {
			return err
		}
		for key := range requested {
			if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission)
				VALUES($1::uuid,$2::uuid,$3)`, creator.TenantID, roleID, key); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
			VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, creator.TenantID, agentID, roleID); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, actor, events.Change{Type: "authz.agent_binding_created",
			After: map[string]any{"principal_id": agentID, "role_id": roleID, "permissions": scopes}})
		return err
	}
	agent := tenant.Principal{ID: agentID, TenantID: creator.TenantID, Kind: tenant.Agent}
	effective, err := authz.EffectiveTx(ctx, tx, agent, "")
	if err != nil {
		return err
	}
	if !builtin && roleKey == "agent_"+strings.ReplaceAll(agentID, "-", "") {
		for key := range requested {
			if slices.Contains(effective.Workspace.Permissions, key) {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission)
					VALUES($1::uuid,$2::uuid,$3) ON CONFLICT DO NOTHING`, creator.TenantID, roleID, key); err != nil {
				return err
			}
			if _, err := events.Append(ctx, tx, actor, events.Change{Type: "authz.agent_permission_granted",
				After: map[string]any{"principal_id": agentID, "role_id": roleID, "permission": key}}); err != nil {
				return err
			}
		}
		return nil
	}
	for key := range requested {
		if !slices.Contains(effective.Workspace.Permissions, key) {
			return authz.ErrForbidden
		}
	}
	return nil
}

func (m *Module) listAgentKeys(ctx context.Context, tenantID string) ([]keyRecord, error) {
	var out []keyRecord
	err := m.inTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, principal_id::text, name, prefix, scopes, created_at, expires_at, last_used_at, revoked_at
			FROM agent_keys
			ORDER BY created_at DESC, id
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rec keyRecord
			var scopes pgtype.FlatArray[string]
			if err := rows.Scan(&rec.ID, &rec.PrincipalID, &rec.Name, &rec.Prefix, &scopes, &rec.CreatedAt, &rec.ExpiresAt, &rec.LastUsedAt, &rec.RevokedAt); err != nil {
				return err
			}
			rec.Scopes = []string(scopes)
			if rec.Scopes == nil {
				rec.Scopes = []string{}
			}
			out = append(out, rec)
		}
		return rows.Err()
	})
	if out == nil {
		out = []keyRecord{}
	}
	return out, err
}

func (m *Module) revokeAgentKey(ctx context.Context, p tenant.Principal, id string) error {
	return m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		return m.revokeAgentKeyTx(ctx, tx, p, id)
	})
}

func (m *Module) revokeAgentKeyTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) error {
	actorID := p.ID
	via := "api"
	if actorID == "" {
		via = "operator"
		var err error
		actorID, err = operatoractor.Ensure(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
	}
	rec, err := lockAgentKey(ctx, tx, id)
	if err != nil {
		return err
	}
	if rec.RevokedAt != nil {
		return nil
	}
	before := keySnapshot(rec)
	if err := tx.QueryRow(ctx, `UPDATE agent_keys SET revoked_at=now() WHERE id=$1::uuid RETURNING revoked_at`, id).Scan(&rec.RevokedAt); err != nil {
		return err
	}
	after := keySnapshot(rec)
	after["via"] = via
	_, err = events.Append(ctx, tx, tenant.Principal{ID: actorID, TenantID: p.TenantID}, events.Change{
		Type: "agent_key.revoked", Before: before, After: after,
	})
	return err
}

func lockAgentKey(ctx context.Context, tx pgx.Tx, id string) (keyRecord, error) {
	var rec keyRecord
	err := tx.QueryRow(ctx, `SELECT id::text,principal_id::text,name,prefix,scopes,created_at,expires_at,last_used_at,revoked_at
		FROM agent_keys WHERE id=$1::uuid FOR UPDATE`, id).Scan(&rec.ID, &rec.PrincipalID, &rec.Name, &rec.Prefix, &rec.Scopes, &rec.CreatedAt, &rec.ExpiresAt, &rec.LastUsedAt, &rec.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNotFound
	}
	return rec, err
}

func keySnapshot(rec keyRecord) map[string]any {
	return map[string]any{"key_id": rec.ID, "principal_id": rec.PrincipalID, "name": rec.Name, "prefix": rec.Prefix,
		"scopes": rec.Scopes, "created_at": rec.CreatedAt, "expires_at": rec.ExpiresAt, "last_used_at": rec.LastUsedAt, "revoked_at": rec.RevokedAt}
}

var errKeyRevoked = errors.New("agent key already revoked")

func (m *Module) rotateAgentKey(ctx context.Context, p tenant.Principal, id string, expires *time.Time) (keyRecord, error) {
	var replacement keyRecord
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Match creation/access-management lock order: tenant, then resource.
		// Serializing on the tenant also fences concurrent grants and rotation.
		var tenantID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR UPDATE`, p.TenantID).Scan(&tenantID); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "keys.manage", authz.Scope{}); err != nil {
			return err
		}
		old, err := lockAgentKey(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.RevokedAt != nil {
			return errKeyRevoked
		}
		scopes, err := cleanScopes(old.Scopes)
		if err != nil {
			return authz.ErrForbidden
		}
		replacement, err = m.createAgentKeyTx(ctx, tx, p, old.Name, old.PrincipalID, scopes, expires)
		if err != nil {
			return err
		}
		return m.revokeAgentKeyTx(ctx, tx, p, old.ID)
	})
	return replacement, err
}

type meView struct {
	Email     *string
	Principal tenant.Principal
	TenantID  string
	Slug      string
	Name      string
	Identity  *identityView
}

type identityView struct {
	ID          string
	Issuer      string
	Subject     string
	Email       *string
	DisplayName *string
}

func (m *Module) loadMe(ctx context.Context, p tenant.Principal) (meView, error) {
	view := meView{Principal: p, TenantID: p.TenantID}
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text,slug,name FROM tenants WHERE id=$1::uuid`, p.TenantID).Scan(&view.TenantID, &view.Slug, &view.Name); err != nil {
			return err
		}
		fresh, err := displayPrincipal(ctx, tx, p.TenantID, p.ID)
		if err != nil {
			return err
		}
		view.Principal = fresh
		var iid, issuer, subject *string
		var email, display *string
		err = tx.QueryRow(ctx, `
			SELECT i.id::text, i.issuer, i.subject,
			    COALESCE(NULLIF(profile.email,''),profile_identity.email),
			    CASE WHEN pr.linked_to IS NOT NULL THEN profile.name ELSE i.display_name END
			FROM principals pr
			JOIN principals profile ON profile.tenant_id=pr.tenant_id
			    AND profile.id=COALESCE(pr.linked_to,pr.id)
			LEFT JOIN identities i ON i.id=pr.identity_id
			LEFT JOIN identities profile_identity ON profile_identity.id=profile.identity_id
			WHERE pr.tenant_id=$2::uuid AND pr.id=$1::uuid
		`, p.ID, p.TenantID).Scan(&iid, &issuer, &subject, &email, &display)
		if err != nil {
			return err
		}
		view.Email = email
		if iid != nil {
			view.Identity = &identityView{
				ID:          *iid,
				Issuer:      deref(issuer),
				Subject:     deref(subject),
				Email:       email,
				DisplayName: display,
			}
		}
		return nil
	})
	return view, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Links change presentation only: retain the authenticated ID, kind and roles.
// B4 enforces one-hop, same-tenant links to person targets.
func displayPrincipal(ctx context.Context, tx pgx.Tx, tenantID, id string) (tenant.Principal, error) {
	return scanPrincipal(tx.QueryRow(ctx, `
		SELECT p.id::text,p.tenant_id::text,p.kind,COALESCE(target.name,p.name),p.roles
		FROM principals p
		LEFT JOIN principals target ON target.tenant_id=p.tenant_id AND target.id=p.linked_to
		WHERE p.tenant_id=$1::uuid AND p.id=$2::uuid AND p.status='active'
	`, tenantID, id))
}
