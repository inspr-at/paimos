// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// StoreNativeContextTx is the application entry to the single database store.
// The caller holds tenant -> hierarchy -> record locks, before event writes.
// A nil role inherits history; a role claims the entire durable alias component.
// Table triggers use the same store so a missed caller cannot skip ownership.
func StoreNativeContextTx(ctx context.Context, tx pgx.Tx, harness string, ref, vendor []byte, person *string, project string, role *string) error {
	_, err := tx.Exec(ctx, `SELECT aeon_store_chat_native(NULLIF(current_setting('aeon.tenant_id',true),'')::uuid,$1,ARRAY[$2::bytea,$3::bytea],$4::uuid,$5::uuid,$6::uuid)`, harness, ref, vendor, person, project, role)
	return nativeContextError(err)
}

func nativeContextError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.ConstraintName == "chat_native_owner" {
		return workorders.Fail(409, "chat binding unavailable")
	}
	return err
}
