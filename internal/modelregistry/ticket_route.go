// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// KnownRouteRole reports whether name is a doctrine route role.
func KnownRouteRole(name string) bool {
	_, ok := roleByName(strings.TrimSpace(name))
	return ok
}

// KnownRouteArea reports whether name is a planning area.
func KnownRouteArea(name string) bool {
	switch strings.TrimSpace(name) {
	case "backend", "frontend", "full-stack", "infra", "design", "docs":
		return true
	default:
		return false
	}
}

// TicketRoute is the registry selection for one ticket role and area.
// Callers display it and do not store the profile on the ticket.
type TicketRoute struct {
	Role            string
	Area            string
	Profile         Profile
	CommandTemplate string
}

// ResolveTicketRoute reads the role ladder. The ladder is keyed by role;
// area must be a known planning area and does not select a different profile.
// A blank or unknown value, review-gate (the ticket stores no author family),
// or a ladder with nothing selected returns nil. Database errors are returned.
// This does not seed the catalog and does not write.
func ResolveTicketRoute(ctx context.Context, tx pgx.Tx, role, area string, now time.Time) (*TicketRoute, error) {
	role = strings.TrimSpace(role)
	area = strings.TrimSpace(area)
	if !KnownRouteRole(role) || !KnownRouteArea(area) || role == "review-gate" {
		return nil, nil
	}
	res, err := resolveRole(ctx, tx, resolveQuery{Role: role}, now)
	if err != nil {
		return nil, err
	}
	if res.Profile == nil {
		return nil, nil
	}
	return &TicketRoute{
		Role: role, Area: area, Profile: *res.Profile, CommandTemplate: res.CommandTemplate,
	}, nil
}
