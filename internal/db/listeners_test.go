// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Risk: slow consumers block the global listener or silently drop event-ID
// dependent wakes; a tenant must never receive another tenant's notification.
func TestNotificationFanoutOverflowAndIsolation(t *testing.T) {
	h := &notificationHub{channel: "aeon_events", subs: make(map[*notificationSubscription]struct{})}
	sub := func(tenant string) *notificationSubscription {
		s := &notificationSubscription{hub: h, tenant: tenant, wake: make(chan *pgconn.Notification, 16), done: make(chan struct{})}
		h.subs[s] = struct{}{}
		return s
	}
	slow, fast, other := sub("one"), sub("one"), sub("two")
	for i := range 17 {
		h.publish(&pgconn.Notification{Channel: h.channel, Payload: fmt.Sprintf(`{"tenant_id":"one","id":%d}`, i+1)})
		n, err := fast.WaitForNotification(t.Context())
		if err != nil || IsResync(n) {
			t.Fatalf("fast consumer lost its hint: %v %v", n, err)
		}
	}
	n, err := slow.WaitForNotification(t.Context())
	if err != nil || !IsResync(n) {
		t.Fatalf("overflow must request resync: %v %v", n, err)
	}
	if len(other.wake) != 0 {
		t.Fatal("cross-tenant notification")
	}
	h.publish(nil)
	for _, s := range []*notificationSubscription{slow, fast, other} {
		n, err := s.WaitForNotification(t.Context())
		if err != nil || !IsResync(n) {
			t.Fatal("connection gap did not request rescan")
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fast.WaitForNotification(canceled); err != context.Canceled {
		t.Fatal("canceled subscriber kept consuming")
	}
}
