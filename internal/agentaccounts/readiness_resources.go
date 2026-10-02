// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/jackc/pgx/v5"
)

// Reconcile only identities already authorized by a person, plus the deliberately
// conservative OpenRouter default endpoint bucket. Local key caps stay distinct.
// Existing memberships, reservations and history are never retargeted or erased.
func reconcileReadinessResources(ctx context.Context, tx pgx.Tx, a Account) error {
	// A withdrawn pool loses launch authority, while its fact/run history stays.
	if _, err := tx.Exec(ctx, `DELETE FROM account_readiness_memberships m USING account_readiness_resources r
 WHERE m.account_id=$1 AND r.tenant_id=m.tenant_id AND r.id=m.resource_id AND r.identity_kind='person_confirmed'
 AND r.identity_key<>encode(sha256(convert_to('quota:'||$2||':'||$3,'UTF8')),'hex')`, a.ID, a.Harness, a.QuotaPoolFingerprint); err != nil {
		return err
	}
	kind, identity, key := "", "", ""
	if a.Harness != "pi" && a.QuotaPoolFingerprint != "" {
		kind, identity, key = "subscription_quota", "person_confirmed", "quota:"+a.Harness+":"+a.QuotaPoolFingerprint
	} else if a.Harness == "pi" && a.Provider == "openrouter" {
		// The approved provider currently has one fixed endpoint. No management
		// credential, key hash or person's identity is used to infer a balance.
		kind, identity, key = "shared_balance", "unresolved", "provider:openrouter:default-endpoint:unresolved"
	} else {
		return nil
	}
	sum := sha256.Sum256([]byte(key))
	var resource string
	err := tx.QueryRow(ctx, `INSERT INTO account_readiness_resources(tenant_id,kind,identity_kind,identity_key)
 SELECT tenant_id,$2,$3,$4 FROM agent_accounts WHERE id=$1
 ON CONFLICT(tenant_id,kind,identity_kind,identity_key) DO UPDATE SET identity_key=EXCLUDED.identity_key RETURNING id::text`, a.ID, kind, identity, hex.EncodeToString(sum[:])).Scan(&resource)
	if err != nil {
		return err
	}
	query := `SELECT tenant_id,id,link_revision FROM agent_accounts WHERE harness='pi' AND provider='openrouter' ORDER BY id LIMIT 257`
	args := []any{}
	if identity == "person_confirmed" {
		query = `SELECT tenant_id,id,link_revision FROM agent_accounts WHERE id IN (` + quotaAccounts + `) ORDER BY id LIMIT 257`
		args = append(args, a.ID)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	type member struct {
		tenant, account string
		revision        int64
	}
	members := []member{}
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.tenant, &m.account, &m.revision); err != nil {
			rows.Close()
			return err
		}
		members = append(members, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(members) > 256 {
		return fail(503, "too many shared readiness members")
	}
	for _, m := range members {
		if _, err := tx.Exec(ctx, `INSERT INTO account_readiness_memberships(tenant_id,account_id,resource_id,binding_revision) VALUES($1,$2,$3,$4)
 ON CONFLICT(tenant_id,account_id,resource_id) DO UPDATE SET binding_revision=EXCLUDED.binding_revision`, m.tenant, m.account, resource, m.revision); err != nil {
			return err
		}
	}
	return nil
}

func vendorReadinessResource(ctx context.Context, tx pgx.Tx, a Account) (string, error) {
	if a.Harness != "pi" && a.QuotaPoolFingerprint != "" {
		sum := sha256.Sum256([]byte("quota:" + a.Harness + ":" + a.QuotaPoolFingerprint))
		var id string
		err := tx.QueryRow(ctx, `SELECT r.id::text FROM account_readiness_resources r JOIN account_readiness_memberships m ON m.tenant_id=r.tenant_id AND m.resource_id=r.id
 WHERE m.account_id=$1 AND m.binding_revision=$2 AND r.identity_kind='person_confirmed' AND r.identity_key=$3`, a.ID, a.LinkRevision, hex.EncodeToString(sum[:])).Scan(&id)
		if err == nil || !isNoRows(err) {
			return id, err
		}
	}
	return localReadinessResource(ctx, tx, a)
}
