// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"reflect"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Progress timestamps and sequence counters prove freshness, not a new state.
// Keep actual activity, metadata and estimate changes in history. Dedicated
// pause, recovery and activity histories continue to record their own changes.
func heartbeatState(s Session) any {
	return struct {
		Phase, Activity                                                                                          string
		Note, Doing, ToolActivity, Model, Effort, Label, Account, Version, Brief, Worktree, Branch, BindingError *string
		Commits                                                                                                  []Commit
		Ready, Live                                                                                              any
		Progress                                                                                                 *int
	}{s.Phase, s.Activity, s.ActivityNote, s.doing, s.toolActivity, s.Model, s.ReasoningEffort, s.DisplayLabel,
		s.AccountLabel, s.HarnessVersion, s.Brief, s.Worktree, s.Branch, s.inboxBindingCode,
		s.Commits, s.EtaReadyAt, s.EtaLiveAt, s.ProgressPct}
}

func recordHeartbeat(ctx context.Context, tx pgx.Tx, p tenant.Principal, before, after Session) error {
	if !reflect.DeepEqual(heartbeatState(before), heartbeatState(after)) {
		return record(ctx, tx, p, after, "heartbeat", before, after)
	}
	// PostgreSQL delivers this value-free wake only after the session update
	// commits. It never allocates an event ID or takes the event-counter lock.
	return events.NotifyHarnessHeartbeat(ctx, tx, p.TenantID, after.ID)
}
