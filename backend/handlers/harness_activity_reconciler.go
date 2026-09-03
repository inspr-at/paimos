// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"log"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
)

const harnessActivityReconcileInterval = 30 * time.Second
const harnessActivityReconcileTimeout = 10 * time.Second

// StartHarnessActivityReconciler owns the server-side freshness transition.
// Adapter silence never asserts idle; an expired authenticated heartbeat can
// only degrade reported activity to unknown.
func StartHarnessActivityReconciler() {
	service := managedharness.NewService(db.DB)
	go func() {
		reconcile := func() {
			ctx, cancel := context.WithTimeout(context.Background(), harnessActivityReconcileTimeout)
			defer cancel()
			if _, err := service.ReconcileStaleActivity(ctx, time.Now().UTC(), managedharness.DefaultActivityHeartbeatTimeout); err != nil {
				log.Printf("harness activity reconcile: %v", err)
			}
		}
		reconcile()
		ticker := time.NewTicker(harnessActivityReconcileInterval)
		defer ticker.Stop()
		for range ticker.C {
			reconcile()
		}
	}()
}
