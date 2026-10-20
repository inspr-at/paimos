// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const StartOnComputer = "run.start_on_computer"

// The subject predicate is the reader seam for additive group subjects in
// S1-09. Until then only a person's own binding is a subject. Keep this shared
// by the decision and the summary; computer/node bindings never enter readGrants.
const computerStartBindingsSQL = `EXISTS (
 SELECT 1 FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
 WHERE b.tenant_id=p.tenant_id AND b.principal_id=p.id
 AND (b.scope_type='workspace' OR b.scope_type='computer' AND b.computer_id=$2::uuid)
 AND ((r.builtin AND r.key=ANY($3::text[])) OR (NOT r.builtin AND EXISTS (
  SELECT 1 FROM role_permissions rp WHERE rp.tenant_id=r.tenant_id AND rp.role_id=r.id AND rp.permission=$4))))`

func computerStartBuiltinRoles() []string {
	roles := []string{}
	for _, key := range builtinKeys {
		if contains(builtinPermissions(key), StartOnComputer) {
			roles = append(roles, key)
		}
	}
	return roles
}

// CanStartOnComputerTx reads current person and role state in the caller's
// tenant transaction. Writers must already hold the tenant/tree access fence,
// before their resource locks, and retain it through commit. This takes no
// further lock, grants no agent authority, and never infers a person from pairing.
func CanStartOnComputerTx(ctx context.Context, tx pgx.Tx, person tenant.Principal, computerID string) (bool, error) {
	if person.Kind != tenant.Person || person.ID == "" || computerID == "" {
		return false, nil
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM principals p
 WHERE p.tenant_id=$1::uuid AND p.id=$5::uuid AND p.kind='person' AND p.status='active' AND p.linked_to IS NULL
 AND EXISTS (SELECT 1 FROM agent_pairing_computers c WHERE c.tenant_id=p.tenant_id AND c.id=$2::uuid)
 AND `+computerStartBindingsSQL+`)`, person.TenantID, computerID, computerStartBuiltinRoles(), StartOnComputer, person.ID).Scan(&allowed)
	return allowed, err
}

// RequireComputerStartForPersonTx resolves the computer from its daemon's
// authenticated principal, never from a caller-supplied computer identifier.
// Non-paired legacy targets keep their existing creation behavior.
func RequireComputerStartForPersonTx(ctx context.Context, tx pgx.Tx, person tenant.Principal, daemonPrincipalID string) error {
	var computer string
	err := tx.QueryRow(ctx, `SELECT id::text FROM agent_pairing_computers WHERE tenant_id=$1 AND principal_id=$2`, person.TenantID, daemonPrincipalID).Scan(&computer)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	allowed, err := CanStartOnComputerTx(ctx, tx, person, computer)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

// RequireRunStartOnComputerTx is the final claim check for all run claim paths.
// NULL acting people are the explicit dual-read compatibility case until S1-07.
// A bound person with no claiming computer fails closed, including delegated
// claims made by a different daemon than the run's assigned principal.
func RequireRunStartOnComputerTx(ctx context.Context, tx pgx.Tx, daemon tenant.Principal, runID string) error {
	var personID, computerID *string
	err := tx.QueryRow(ctx, `SELECT r.acting_person_id::text,
 (SELECT c.id::text FROM agent_pairing_computers c WHERE c.tenant_id=r.tenant_id AND c.principal_id=$3)
 FROM agent_runs r WHERE r.tenant_id=$1 AND r.id=$2`, daemon.TenantID, runID, daemon.ID).Scan(&personID, &computerID)
	if err != nil {
		return err
	}
	if personID == nil {
		return nil
	}
	if computerID == nil || daemon.Kind != tenant.Agent {
		return ErrForbidden
	}
	allowed, err := CanStartOnComputerTx(ctx, tx, tenant.Principal{TenantID: daemon.TenantID, ID: *personID, Kind: tenant.Person}, *computerID)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

type ComputerStartPerson struct {
	PrincipalID string `json:"principal_id"`
	Name        string `json:"name"`
}

// ComputerStartPeopleTx uses the same subject/role reader as admission. The
// Admins label covers workspace Owner/Admin; explicit and custom workspace
// grants are listed by person. limit+1 permits honest keyset continuation.
func ComputerStartPeopleTx(ctx context.Context, tx pgx.Tx, tenantID, computerID, cursor string, limit int) ([]ComputerStartPerson, *string, error) {
	if limit < 1 || limit > 100 {
		return nil, nil, ErrForbidden
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text,left(p.name,200) FROM principals p
 WHERE p.tenant_id=$1::uuid AND p.kind='person' AND p.status='active' AND p.linked_to IS NULL
 AND ($5::uuid IS NULL OR p.id>$5::uuid) AND `+computerStartBindingsSQL+`
 AND NOT EXISTS (SELECT 1 FROM role_bindings wb JOIN roles wr ON wr.tenant_id=wb.tenant_id AND wr.id=wb.role_id
 WHERE wb.tenant_id=p.tenant_id AND wb.principal_id=p.id AND wb.scope_type='workspace' AND wr.builtin AND wr.key=ANY($3::text[]))
 ORDER BY p.id LIMIT $6`, tenantID, computerID, computerStartBuiltinRoles(), StartOnComputer, nilIfEmpty(cursor), limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	people := []ComputerStartPerson{}
	for rows.Next() {
		var person ComputerStartPerson
		if err := rows.Scan(&person.PrincipalID, &person.Name); err != nil {
			return nil, nil, err
		}
		people = append(people, person)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *string
	if len(people) > limit {
		people = people[:limit]
		last := people[limit-1].PrincipalID
		next = &last
	}
	return people, next, nil
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
