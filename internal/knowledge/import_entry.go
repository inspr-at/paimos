// SPDX-License-Identifier: AGPL-3.0-only
package knowledge

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// CreateImportedMemoryTx creates the memory entry a conversation handed over
// from START brings into its new project (AEON-1441). It runs the ordinary
// create parsing, slug guard and knowledge.write check in the project, and
// returns the events for the caller to append after its remaining locks.
func CreateImportedMemoryTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, slug, title, body, keyPrefix string, metadata map[string]any) (string, []events.Change, error) {
	raw := map[string]json.RawMessage{}
	for key, value := range map[string]any{"project_id": projectID, "type": "memory", "slug": slug, "title": title, "body": body, "status": statusActive, "metadata": metadata, "key_prefix": keyPrefix} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", nil, err
		}
		raw[key] = encoded
	}
	in, err := parseCreate(raw)
	pending := []events.Change{}
	var entry Entry
	if err == nil {
		entry, err = createTx(ctx, tx, p, in, &pending)
	}
	var e *apiError
	if errors.As(err, &e) {
		return "", nil, workorders.Fail(e.status, e.msg)
	}
	return entry.ID, pending, err
}
