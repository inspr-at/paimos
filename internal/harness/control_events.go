// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type controlEventKey struct{}
type controlEvent struct {
	principal tenant.Principal
	project   string
	kind      string
	before    json.RawMessage
	after     json.RawMessage
}
type controlEventBatch struct{ events []controlEvent }

// Tier decisions and daemon control batches can update history after expiring,
// claiming or completing controls. Save their snapshots now, but acquire the
// tenant event counter only after every record write has finished. An error
// discards the batch; flushing still belongs to the same mutation transaction.
func deferControlEvents(r *http.Request, tx pgx.Tx) (*http.Request, func(*error)) {
	batch := &controlEventBatch{}
	r = r.WithContext(context.WithValue(r.Context(), controlEventKey{}, batch))
	return r, func(result *error) {
		if *result != nil {
			return
		}
		for _, event := range batch.events {
			if err := workorders.Record(r.Context(), tx, event.principal, event.project, "harness."+event.kind, event.before, event.after); err != nil {
				*result = err
				return
			}
		}
	}
}

func (b *controlEventBatch) add(p tenant.Principal, s Session, kind string, before, after any) error {
	// The session control quota is 16. Leave room for claim/completion/expiry
	// and session events, while keeping a future accidental loop bounded.
	if len(b.events) >= 64 {
		return workorders.Fail(429, "control event batch limit reached")
	}
	old, err := json.Marshal(before)
	if err != nil {
		return err
	}
	next, err := json.Marshal(after)
	if err != nil {
		return err
	}
	b.events = append(b.events, controlEvent{p, s.ProjectID, kind, old, next})
	return nil
}
