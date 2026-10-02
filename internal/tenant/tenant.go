// SPDX-License-Identifier: AGPL-3.0-only

// Package tenant carries the acting principal and its tenant through a request.
// Shared contract between P0.2 (core) and P0.3 (auth); extend, do not rename.
package tenant

import (
	"context"
)

// PrincipalKind distinguishes people from agents; both are first-class.
type PrincipalKind string

const (
	Person PrincipalKind = "person"
	Agent  PrincipalKind = "agent"
)

// Principal is who acts, always inside exactly one tenant.
type Principal struct {
	ID                    string // principals.id (uuid)
	TenantID              string // tenants.id (uuid)
	Kind                  PrincipalKind
	Name                  string
	Roles                 []string // e.g. "admin", "member"
	Scopes                []string // authenticated agent key's outer permission ceiling
	KeyCreatorID          string   // creator's live binding further narrows an agent key
	KeyID                 string   // authenticated key identity, never the bearer secret
	OwnerWorkstation      bool     // explicit designation; authz rechecks live binding
	WorkstationComputerID string
	WorkstationGeneration int64
}

type ctxKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom returns the principal set by the auth middleware, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
