// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"reflect"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Progress timestamps and sequence counters prove freshness, not a new state.
// Keep actual activity, metadata and estimate changes in history. Dedicated
// pause, recovery and activity histories continue to record their own changes.
type heartbeatEventState struct {
	phase, activity                string
	note, doing, toolActivity      *string
	model, effort, label           *string
	account, version, brief        *string
	worktree, branch, bindingError *string
	commits                        []Commit
	processOwnership               *ownedprocess.Identity
	capabilities                   []string
	ready, live                    *time.Time
	progress                       *int
}

func heartbeatState(s Session) heartbeatEventState {
	capabilities := append([]string(nil), s.Capabilities...)
	slices.Sort(capabilities)
	return heartbeatEventState{
		phase: s.Phase, activity: s.Activity,
		note: s.ActivityNote, doing: s.doing, toolActivity: s.toolActivity,
		model: s.Model, effort: s.ReasoningEffort, label: s.DisplayLabel,
		account: s.AccountLabel, version: s.HarnessVersion, brief: s.Brief,
		worktree: s.Worktree, branch: s.Branch, bindingError: s.inboxBindingCode,
		commits: s.Commits, ready: s.EtaReadyAt, live: s.EtaLiveAt, progress: s.ProgressPct,
		processOwnership: s.ProcessOwnership, capabilities: capabilities,
	}
}

func recordHeartbeat(ctx context.Context, tx pgx.Tx, p tenant.Principal, before, after Session) error {
	if !reflect.DeepEqual(heartbeatState(before), heartbeatState(after)) {
		return record(ctx, tx, p, after, "heartbeat", before, after)
	}
	// PostgreSQL delivers this value-free wake only after the session update
	// commits. It never allocates an event ID or takes the event-counter lock.
	return events.NotifyHarnessHeartbeat(ctx, tx, p.TenantID, after.ID)
}
