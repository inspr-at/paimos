// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"strings"

	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
)

// KnownRouteRole reports whether name is a doctrine route role.
func KnownRouteRole(name string) bool {
	_, ok := roleByName(strings.TrimSpace(name))
	return ok
}

// KnownRouteArea accepts active default and project kinds. The review and
// catch-all matrix rows are never ticket areas.
func KnownRouteArea(ctx context.Context, tx pgx.Tx, name, project string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "review" || name == "other" {
		return false, nil
	}
	_, fallback, err := modelprefs.LookupKind(ctx, tx, name, project)
	return !fallback, err
}
