package handlers

import (
	"database/sql"
	"errors"
	"strings"
)

// database/sql does not export its closed-DB sentinel, unlike ErrConnDone.
// Both errors are terminal for background work bound to that handle.
func isClosedDatabaseError(err error) bool {
	return errors.Is(err, sql.ErrConnDone) ||
		(err != nil && strings.Contains(err.Error(), "sql: database is closed"))
}
