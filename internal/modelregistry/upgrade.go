// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"

	"github.com/inspr-at/paimos/internal/modelactivation"
	"github.com/jackc/pgx/v5"
)

func catalogLock(ctx context.Context, tx pgx.Tx) error {
	return modelactivation.Lock(ctx, tx)
}

// KnownInvalid is a verified retired/invalid identifier, not a vendor response.
func KnownInvalid(model string) bool {
	switch model {
	case "gpt-6-terra", "gpt-6.1-astra", "gpt-6.1-luna", "gpt-6.1-terra":
		return true
	}
	return false
}
