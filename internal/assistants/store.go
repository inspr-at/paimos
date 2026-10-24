// SPDX-License-Identifier: AGPL-3.0-only

package assistants

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const assistantSelect = `SELECT p.id::text,coalesce(o.linked_to,o.id)::text,p.status,
 coalesce(a.revision,0),a.policy,p.name,coalesce(a.vendor,'unknown'),coalesce(p.messaging_only,false)
 FROM principals p LEFT JOIN assistant_profiles a ON a.tenant_id=p.tenant_id AND a.principal_id=p.id
 JOIN principals o ON o.tenant_id=p.tenant_id AND o.id=coalesce(a.owner_person_id,p.messaging_owner_id)
 WHERE p.tenant_id=$1::uuid AND p.kind='agent' AND
 (p.agent_class='assistant' OR p.messaging_only IS TRUE) AND o.kind='person'`

func scanAssistant(row pgx.Row) (Assistant, error) {
	out := Assistant{Kind: "assistant", PrincipalKind: "agent"}
	var policy []byte
	err := row.Scan(&out.PrincipalID, &out.OwnerPersonID, &out.Status, &out.Revision, &policy, &out.DisplayName, &out.Vendor, &out.MessagingOnly)
	if errors.Is(err, pgx.ErrNoRows) {
		return Assistant{}, ErrNotFound
	}
	if err != nil {
		return Assistant{}, err
	}
	out.Policy = DefaultPolicy()
	if len(policy) > 0 {
		out.Policy = Policy{}
		if err := json.Unmarshal(policy, &out.Policy); err != nil {
			return Assistant{}, err
		}
		out.Policy, err = cleanPolicy(out.Policy)
		if err != nil {
			return Assistant{}, err
		}
	}
	return out, nil
}

func getTx(ctx context.Context, tx pgx.Tx, tenantID, principalID string) (Assistant, error) {
	return scanAssistant(tx.QueryRow(ctx, assistantSelect+` AND p.id=$2::uuid`, tenantID, principalID))
}

func (m *Module) ReadPolicy(ctx context.Context, tenantID, principalID string) (Snapshot, error) {
	if err := policyReadInput(ctx, tenantID, principalID); err != nil {
		return Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out Snapshot
	err := db.InTenantReadSnapshot(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = m.ReadPolicyTx(ctx, tx, tenantID, principalID)
		return err
	})
	if err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

// ReadPolicyTx reads the policy in the caller's existing tenant transaction.
// The caller owns the tenant fence when this policy authorizes a mutation.
func (m *Module) ReadPolicyTx(ctx context.Context, tx pgx.Tx, tenantID, principalID string) (Snapshot, error) {
	if err := policyReadInput(ctx, tenantID, principalID); err != nil {
		return Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := getTx(ctx, tx, tenantID, principalID)
	if err != nil {
		return Snapshot{}, err
	}
	return out.Snapshot, nil
}

func policyReadInput(ctx context.Context, tenantID, principalID string) error {
	if !uuid.MatchString(tenantID) || !uuid.MatchString(principalID) {
		return ErrNotFound
	}
	if p, ok := tenant.PrincipalFrom(ctx); ok && !strings.EqualFold(p.TenantID, tenantID) {
		return authz.ErrForbidden
	}
	return nil
}

func readAuthority(ctx context.Context, tx pgx.Tx, p tenant.Principal, policyOnly bool, principalID string) error {
	if p.Kind == tenant.Agent {
		if !policyOnly || !strings.EqualFold(p.ID, principalID) {
			return authz.ErrForbidden
		}
		return authz.RequireTx(ctx, tx, p, "inbox.read", authz.Scope{})
	}
	if p.Kind != tenant.Person {
		return authz.ErrForbidden
	}
	return authz.RequireTx(ctx, tx, p, "members.read", authz.Scope{})
}

func writeAuthority(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Person {
		return authz.ErrForbidden
	}
	// Same tenant/tree fences as access changes, before permissions and rows.
	if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
		return err
	}
	for _, permission := range []string{"agents.create", "keys.manage"} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{}); err != nil {
			return err
		}
	}
	return nil
}

func requireOwnerTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, ownerID string) error {
	var callerID string
	if err := tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals
 WHERE tenant_id=$1::uuid AND id=$2::uuid AND kind='person' AND status='active'`, p.TenantID, p.ID).Scan(&callerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return authz.ErrForbidden
		}
		return err
	}
	if callerID != ownerID {
		return authz.ErrForbidden
	}
	return nil
}

func validateProjectsTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, policy Policy) error {
	for _, id := range append(append([]string{}, policy.ApproverPersonIDs...), policy.DelegatorPersonIDs...) {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid AND kind='person' AND status='active' AND linked_to IS NULL)`, p.TenantID, id).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return errInput
		}
	}
	for _, id := range policy.AllowedProjects {
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: id}); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k
 ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1::uuid AND n.id=$2::uuid
 AND k.slug='project' AND n.deleted_at IS NULL)`, p.TenantID, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errInput
		}
	}
	return nil
}

func saveProfileTx(ctx context.Context, tx pgx.Tx, tenantID string, out *Assistant) error {
	policy, err := json.Marshal(out.Policy)
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO assistant_profiles(tenant_id,principal_id,owner_person_id,vendor,policy)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::jsonb)
 ON CONFLICT(tenant_id,principal_id) DO UPDATE SET vendor=excluded.vendor,policy=excluded.policy,
 revision=assistant_profiles.revision+1,updated_at=now() RETURNING revision`,
		tenantID, out.PrincipalID, out.OwnerPersonID, out.Vendor, policy).Scan(&out.Revision); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE principals SET agent_class='assistant',name=$3 WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenantID, out.PrincipalID, out.DisplayName)
	return err
}

func nameAvailableTx(ctx context.Context, tx pgx.Tx, tenantID, name, id string) error {
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1::uuid
 AND lower(name)=lower($2) AND id IS DISTINCT FROM NULLIF($3,'')::uuid)`, tenantID, name, id).Scan(&taken); err != nil {
		return err
	}
	if taken {
		return ErrConflict
	}
	return nil
}

func (m *Module) create(ctx context.Context, p tenant.Principal, in createInput) (Assistant, error) {
	out := Assistant{Kind: "assistant", PrincipalKind: "agent", DisplayName: strings.TrimSpace(in.DisplayName), Vendor: in.Vendor, MessagingOnly: true,
		Snapshot: Snapshot{PrincipalID: strings.ToLower(in.PrincipalID), OwnerPersonID: strings.ToLower(in.OwnerPersonID), Status: "active"}}
	var err error
	out.Policy, err = cleanPolicy(in.Policy)
	if err != nil || !validName(out.DisplayName) || !validVendor(out.Vendor) || !uuid.MatchString(out.OwnerPersonID) || out.PrincipalID != "" && !uuid.MatchString(out.PrincipalID) {
		return Assistant{}, errInput
	}
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := writeAuthority(ctx, tx, p); err != nil {
			return err
		}
		var owner string
		if err := tx.QueryRow(ctx, `SELECT coalesce(o.linked_to,o.id)::text FROM principals o JOIN principals c
 ON c.tenant_id=o.tenant_id AND c.id=coalesce(o.linked_to,o.id)
 WHERE o.tenant_id=$1::uuid AND o.id=$2::uuid AND o.kind='person' AND o.status='active'
 AND c.kind='person' AND c.status='active'`, p.TenantID, out.OwnerPersonID).Scan(&owner); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out.OwnerPersonID = owner
		if err := requireOwnerTx(ctx, tx, p, owner); err != nil {
			return err
		}
		if err := validateProjectsTx(ctx, tx, p, out.Policy); err != nil {
			return err
		}
		if out.PrincipalID != "" {
			old, err := getTx(ctx, tx, p.TenantID, out.PrincipalID)
			if errors.Is(err, ErrNotFound) {
				// An explicit person link may classify an already scoped agent.
				// Every usable key must carry that same person grant. No key,
				// messaging-only marker, role or permission is changed here.
				var eligible bool
				err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals a WHERE a.tenant_id=$1::uuid AND a.id=$2::uuid AND a.kind='agent' AND a.status='active' AND NOT coalesce(a.messaging_only,false)
 AND NOT (a.roles && ARRAY['system','importer','operator','embedding','quote_public_service','quote_confirmation_service','portal_public_service']::text[])
 AND EXISTS(SELECT 1 FROM agent_keys k JOIN principals o ON o.tenant_id=k.tenant_id AND o.id=k.person_id WHERE k.tenant_id=a.tenant_id AND k.principal_id=a.id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND coalesce(o.linked_to,o.id)=$3::uuid AND o.kind='person' AND o.status='active')
 AND NOT EXISTS(SELECT 1 FROM agent_keys k LEFT JOIN principals o ON o.tenant_id=k.tenant_id AND o.id=k.person_id WHERE k.tenant_id=a.tenant_id AND k.principal_id=a.id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND (o.id IS NULL OR coalesce(o.linked_to,o.id)<>$3::uuid OR o.kind<>'person' OR o.status<>'active')))`, p.TenantID, out.PrincipalID, owner).Scan(&eligible)
				if err == nil && eligible {
					old = Assistant{Snapshot: Snapshot{OwnerPersonID: owner, Status: "active"}}
				} else if err == nil {
					err = authz.ErrForbidden
				}
			}
			if err != nil {
				return err
			}
			if old.OwnerPersonID != owner {
				return authz.ErrForbidden
			}
			if old.Revision != 0 {
				return ErrConflict
			}
			out.Status = old.Status
			out.MessagingOnly = old.MessagingOnly
		} else {
			if err := nameAvailableTx(ctx, tx, p.TenantID, out.DisplayName, ""); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles,agent_access_configured,messaging_only,messaging_owner_id,agent_class)
 VALUES($1::uuid,'agent',$2,'{}',true,true,$3::uuid,'assistant') RETURNING id::text`, p.TenantID, out.DisplayName, owner).Scan(&out.PrincipalID); err != nil {
				return err
			}
		}
		if err := nameAvailableTx(ctx, tx, p.TenantID, out.DisplayName, out.PrincipalID); err != nil {
			return err
		}
		if err := saveProfileTx(ctx, tx, p.TenantID, &out); err != nil {
			return err
		}
		_, err := events.Append(ctx, tx, p, events.Change{Type: "assistant.created", After: out})
		return err
	})
	return out, err
}

func (m *Module) update(ctx context.Context, p tenant.Principal, id string, in updateInput, policyOnly, disable bool) (Assistant, error) {
	var out Assistant
	if !disable && (in.ExpectedRevision == nil || *in.ExpectedRevision < 0) {
		return out, errInput
	}
	var policy Policy
	if policyOnly {
		if in.Policy == nil {
			return out, errInput
		}
		var err error
		policy, err = cleanPolicy(*in.Policy)
		if err != nil {
			return out, err
		}
	} else if !disable && (in.Policy != nil || in.DisplayName == nil && in.Vendor == nil) {
		return out, errInput
	}
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := writeAuthority(ctx, tx, p); err != nil {
			return err
		}
		var err error
		out, err = getTx(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if err := requireOwnerTx(ctx, tx, p, out.OwnerPersonID); err != nil {
			return err
		}
		before := out
		if !disable && out.Revision != *in.ExpectedRevision {
			return ErrConflict
		}
		if disable {
			if out.Status == "deactivated" {
				return nil
			}
			if _, err := tx.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE tenant_id=$1::uuid AND id=$2::uuid`, p.TenantID, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE agent_keys SET revoked_at=now() WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND revoked_at IS NULL`, p.TenantID, id); err != nil {
				return err
			}
			out.Status = "deactivated"
		} else if policyOnly {
			if err := validateProjectsTx(ctx, tx, p, policy); err != nil {
				return err
			}
			out.Policy = policy
		} else {
			if in.DisplayName != nil {
				out.DisplayName = strings.TrimSpace(*in.DisplayName)
				if !validName(out.DisplayName) {
					return errInput
				}
				if err := nameAvailableTx(ctx, tx, p.TenantID, out.DisplayName, id); err != nil {
					return err
				}
			}
			if in.Vendor != nil {
				out.Vendor = *in.Vendor
				if !validVendor(out.Vendor) {
					return errInput
				}
			}
		}
		if err := saveProfileTx(ctx, tx, p.TenantID, &out); err != nil {
			return err
		}
		action := "assistant.updated"
		if policyOnly {
			action = "assistant.policy_updated"
		}
		if disable {
			action = "assistant.disabled"
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: action, Before: before, After: out})
		return err
	})
	return out, err
}
