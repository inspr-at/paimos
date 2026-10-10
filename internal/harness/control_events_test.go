// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

func TestControlEventBounds(t *testing.T) {
	assertLimit := func(t *testing.T, b *controlEventBatch, message string) {
		t.Helper()
		count, size := len(b.events), b.bytes
		err := b.add(tenant.Principal{}, Session{}, "adopted", nil, nil)
		var failure *workorders.Error
		if !errors.As(err, &failure) || failure.Status != 429 || failure.Message != message {
			t.Fatalf("batch failure=%v, want 429 %q", err, message)
		}
		if len(b.events) != count || b.bytes != size {
			t.Fatal("failed addition changed buffered events or size")
		}
	}
	t.Run("event count", func(t *testing.T) {
		b := &controlEventBatch{events: make([]controlEvent, maxControlEvents)}
		assertLimit(t, b, "control event batch limit reached")
	})
	t.Run("snapshot bytes", func(t *testing.T) {
		b := &controlEventBatch{bytes: maxControlEventBytes - 8}
		if err := b.add(tenant.Principal{}, Session{}, "adopted", nil, nil); err != nil {
			t.Fatal(err)
		}
		if b.bytes != maxControlEventBytes || len(b.events) != 1 {
			t.Fatal("boundary addition did not consume exactly the remaining bytes")
		}
		assertLimit(t, b, "control event batch byte limit reached")
	})
}
