// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Called only while the registration map is read-locked and this computer has
// no key. A wrong existing key never reaches this path. Missing process memory
// is recoverable, but cannot turn a lifecycle/scope refusal into registration.
func (m *Module) unknownWatchKey(ctx context.Context, p tenant.Principal, in attachwatch.DeviceRequest) error {
	rejected := fail(403, "forbidden", "daemon poll key rejected")
	if !uuidRE.MatchString(in.RequestID) || !uuidRE.MatchString(in.Snapshot.ProjectID) || !uuidRE.MatchString(in.Snapshot.TicketID) || !in.Snapshot.Valid() || in.Snapshot.ComputerID != in.ComputerID || in.Digest != in.Snapshot.Digest() {
		return rejected
	}
	if in.Operation != "request" && in.Operation != "poll" && in.Operation != "detach" && in.Operation != "exited" {
		return rejected
	}
	// Recovery has its own computer budget, charged before eligibility reads.
	// A replayed request is charged by the normal registered request path.
	if err := m.watchRecovery.limit(p, time.Now()); err != nil {
		return err
	}
	return m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		owner, principal, host, workspace, _, err := attachComputer(ctx, tx, in.ComputerID)
		if err != nil || principal != p.ID || host != in.Snapshot.Host || !attachwatch.Within(workspace, in.Snapshot.Process.CWD) {
			return rejected
		}
		// Keep diagnostics opaque without poll authority, including scope and
		// enrollment failures. Only a fully eligible computer gets the signal.
		if err = attachScope(ctx, tx, p.TenantID, owner, in.Snapshot); err != nil {
			return rejected
		}
		v, approvedOwner, _, err := loadAttach(ctx, tx, in.RequestID)
		if errors.Is(err, pgx.ErrNoRows) && in.Operation == "request" {
			return attachFailure(403, "forbidden", "daemon poll key rejected", attachwatch.RefusalPollKeyUnknown)
		}
		if err != nil || approvedOwner != owner || v.Snapshot.ComputerID != in.ComputerID || v.Digest != in.Digest {
			return rejected
		}
		var live bool
		if err = tx.QueryRow(ctx, `SELECT (state IN ('pending','approved') AND expires_at>clock_timestamp()) OR (state='active' AND lease_until>clock_timestamp()) FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&live); err != nil {
			return err
		}
		if !live {
			return rejected
		}
		return attachFailure(403, "forbidden", "daemon poll key rejected", attachwatch.RefusalPollKeyUnknown)
	})
}

// Pairing provisions a distinct principal for each computer. Use authenticated
// identity rather than submitted computer IDs, so a caller cannot rotate IDs
// to evade its budget or allocate arbitrary entries. Like poll authority, this
// short-lived budget is local to the server process and survives registration.
const recoveryWindow = time.Minute
const recoveryAttempts = 30
const recoveryCapacity = 4096

type watchRecoveryLimits struct {
	sync.Mutex
	computers map[string]rate
}

type attachRecoveryLimited struct {
	error
	retryAfter int
}

func (e *attachRecoveryLimited) Unwrap() error { return e.error }

func (l *watchRecoveryLimits) limit(p tenant.Principal, now time.Time) error {
	l.Lock()
	defer l.Unlock()
	for key, v := range l.computers {
		if !now.Before(v.start.Add(recoveryWindow)) {
			delete(l.computers, key)
		}
	}
	key := p.TenantID + "/" + p.ID
	v, exists := l.computers[key]
	limited := func(until time.Time) error {
		seconds := max(1, int((until.Sub(now)+time.Second-1)/time.Second))
		return &attachRecoveryLimited{fail(429, "attach_recovery_limited", "computer recovery attempt cap reached; retry after the indicated delay"), seconds}
	}
	if !exists {
		if len(l.computers) >= recoveryCapacity {
			// Tell the caller when the first occupied slot becomes available.
			until := now.Add(recoveryWindow)
			for _, occupied := range l.computers {
				if end := occupied.start.Add(recoveryWindow); end.Before(until) {
					until = end
				}
			}
			return limited(until)
		}
		v.start = now
		if l.computers == nil {
			l.computers = make(map[string]rate)
		}
	}
	if v.n >= recoveryAttempts {
		return limited(v.start.Add(recoveryWindow))
	}
	v.n++
	l.computers[key] = v
	return nil
}
