// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"
	"strings"

	"github.com/inspr-at/paimos/internal/tenant"
)

// PublicRoute marks a matched route that is public. Unknown routes have no
// declaration and must be denied by the authorization boundary. Session
// refresh uses the same marker, so it cannot disagree with authorization.
const PublicRoute = "public"

// AuthenticatedRoute requires a principal resolved by authentication, without
// a role or key scope. It is reserved for the caller's own identity endpoint.
const AuthenticatedRoute = "authenticated"

// RoutePermissions declares the permission for each registered API pattern.
// Customer quote routes accept one of two permissions; the quote handler also
// verifies the recipient binding. Authentication and public capability routes
// remain explicit entries so route coverage can detect new unreviewed paths.
var RoutePermissions = map[string]string{}

func PermissionForPattern(pattern string) (string, bool) {
	permission, ok := RoutePermissions[pattern]
	return permission, ok
}

// PatternIsPublic reports whether the router's matched pattern is a public
// declaration. Session refresh and the authentication gate use this and not
// the request URL: the router accepts other spellings of the same pattern.
func PatternIsPublic(pattern string) bool {
	declaration, ok := PermissionForPattern(pattern)
	return ok && declaration == PublicRoute
}

// RequirePattern denies missing declarations. A public declaration leaves the
// route's own capability or login checks in place. An authenticated declaration
// requires the trusted principal set by authentication. Quote portal declarations
// allow either staff or customer authority; the handler checks ownership.
// Status help is scope-free tenant metadata for agents; people retain nodes.read.
func RequirePattern(ctx context.Context, pattern string, scope Scope) error {
	declaration, ok := PermissionForPattern(pattern)
	if !ok {
		return ErrForbidden
	}
	if declaration == PublicRoute {
		return nil
	}
	if pattern == "GET /api/status/help" {
		p, ok := tenant.PrincipalFrom(ctx)
		if !ok || p.ID == "" || p.TenantID == "" {
			return ErrForbidden
		}
		if p.Kind == tenant.Agent {
			return nil
		}
	}
	if declaration == AuthenticatedRoute {
		if p, ok := tenant.PrincipalFrom(ctx); ok && p.ID != "" && p.TenantID != "" {
			return nil
		}
		return ErrForbidden
	}
	var denialErr error = ErrForbidden
	for _, permission := range strings.Split(declaration, "|") {
		err := Require(ctx, permission, scope)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrForbidden) {
			return err
		}
		// Prefer a missing key scope only after this route alternative's role
		// authority passed; otherwise keep the first denial.
		var d *denial
		if denialErr == ErrForbidden || errors.As(err, &d) && d.reason == "missing_key_scope" {
			denialErr = err
		}
	}
	return denialErr
}
