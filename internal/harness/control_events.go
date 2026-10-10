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

const (
	maxAdoptedChildren = 1000
	// Registration/resume adds one event per live or paused child, plus the
	// predecessor's controls, deliveries and continuation/session events.
	maxControlEvents     = maxAdoptedChildren + 64
	maxControlEventBytes = 32 << 20
)

type controlEventBatch struct {
	events []controlEvent
	bytes  int
}

// Tier decisions and daemon control batches can update history after expiring,
// claiming or completing controls. Save their snapshots now, but acquire the
// tenant event counter only after every record write has finished. An error
// discards the batch; flushing still belongs to the same mutation transaction.
func deferControlEvents(r *http.Request, tx pgx.Tx) (*http.Request, func(*error)) {
	if _, nested := r.Context().Value(controlEventKey{}).(*controlEventBatch); nested {
		return r, func(*error) {}
	}
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
	if len(b.events) >= maxControlEvents {
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
	size := len(old) + len(next)
	if size > maxControlEventBytes-b.bytes {
		return workorders.Fail(429, "control event batch byte limit reached")
	}
	b.events = append(b.events, controlEvent{p, s.ProjectID, kind, old, next})
	b.bytes += size
	return nil
}
