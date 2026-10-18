// SPDX-License-Identifier: AGPL-3.0-only

package nativerecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
)

const journalBytes = 64 << 10

// This is an at-most-once decision journal, not a second process/event log.
// Possible fork and exit authority remain in the existing owner journal/ledger.
type attempt struct {
	RequestID string                 `json:"request_id"`
	Digest    string                 `json:"digest"`
	Previous  nativecontract.Binding `json:"previous"`
	Next      nativecontract.Binding `json:"next"`
	Complete  bool                   `json:"complete"`
	// Missing on older snapshots: expiry alone then cannot authorize removal.
	RetainUntil *time.Time `json:"retain_until,omitempty"`
}

type decisionJournal struct {
	Version   int       `json:"version"`
	TenantID  string    `json:"tenant_id"`
	SessionID string    `json:"owned_session_id"`
	AutoUsed  bool      `json:"auto_used"`
	Attempts  []attempt `json:"attempts"`
}

func journalName(session string) string { return "recovery-" + session + ".json" }

func (c *Coordinator) read(ctx context.Context, b nativecontract.Binding) (decisionJournal, error) {
	out := decisionJournal{Version: 1, TenantID: b.TenantID, SessionID: b.OwnedSessionID, Attempts: []attempt{}}
	raw, err := c.cfg.State.Read(journalName(b.OwnedSessionID), journalBytes)
	if errors.Is(err, os.ErrNotExist) {
		names, err := c.cfg.State.Names(MaxSessions + 1)
		if err != nil {
			return out, err
		}
		// One recovery.lock plus at most 128 conversation decision snapshots.
		if len(names) >= MaxSessions+1 {
			reclaimed, err := c.reclaim(ctx, names)
			if err != nil {
				return out, err
			}
			if !reclaimed {
				return out, fail(nativecontract.ReasonBufferFull)
			}
		}
		return out, nil
	}
	if err != nil {
		return out, err
	}
	return c.decodeJournal(raw, b.TenantID, b.OwnedSessionID)
}

func (c *Coordinator) decodeJournal(raw []byte, tenant, session string) (decisionJournal, error) {
	out := decisionJournal{Version: 1, TenantID: tenant, SessionID: session, Attempts: []attempt{}}
	// Inspect the bounded array as raw bytes before allocating attempts. The
	// file cap and item count both precede decoding local metadata structs.
	var header struct {
		Version   int             `json:"version"`
		TenantID  string          `json:"tenant_id"`
		SessionID string          `json:"owned_session_id"`
		AutoUsed  *bool           `json:"auto_used"`
		Attempts  json.RawMessage `json:"attempts"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&header) != nil || d.Decode(new(any)) != io.EOF || header.Version != 1 ||
		!validUUID(header.TenantID) || header.SessionID != session || !validUUID(session) || header.AutoUsed == nil ||
		tenant != "" && header.TenantID != tenant {
		return out, fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	out.TenantID = header.TenantID
	d = json.NewDecoder(bytes.NewReader(header.Attempts))
	d.DisallowUnknownFields()
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return out, fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	ids := map[string]bool{}
	for d.More() {
		if len(out.Attempts) == MaxAttempts {
			return out, fail(nativecontract.ReasonBufferFull)
		}
		var entry attempt
		if d.Decode(&entry) != nil || !validUUID(entry.RequestID) || ids[entry.RequestID] || !nativereceipts.ValidDigest(entry.Digest) ||
			c.validateBinding(entry.Previous) != nil || c.validateBinding(entry.Next) != nil ||
			entry.Previous.TenantID != out.TenantID || entry.Next.TenantID != out.TenantID ||
			entry.Previous.OwnedSessionID != session || entry.Next.OwnedSessionID != session ||
			entry.Previous.ProjectID != entry.Next.ProjectID || entry.Previous.RuntimeEpoch == entry.Next.RuntimeEpoch ||
			entry.Previous.HarnessSessionID == entry.Next.HarnessSessionID || entry.Next.BindingRevision <= entry.Previous.BindingRevision ||
			entry.RetainUntil != nil && entry.RetainUntil.IsZero() {
			return out, fail(nativecontract.ReasonOwnershipUnconfirmed)
		}
		ids[entry.RequestID] = true
		out.Attempts = append(out.Attempts, entry)
	}
	if token, err := d.Token(); err != nil || token != json.Delim(']') || d.Decode(new(any)) != io.EOF {
		return out, fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	out.AutoUsed = *header.AutoUsed
	return out, nil
}

// Called only under recovery.lock and the owner's mutation fence. Free one
// snapshot before admitting a new conversation; inventory, bytes and decisions
// remain bounded. Unknown files, corrupt snapshots and uncertain attempts stay.
func (c *Coordinator) reclaim(ctx context.Context, names []string) (bool, error) {
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		session := strings.TrimSuffix(strings.TrimPrefix(name, "recovery-"), ".json")
		if !validUUID(session) || journalName(session) != name {
			continue
		}
		raw, err := c.cfg.State.Read(name, journalBytes)
		if err != nil {
			continue
		}
		journal, err := c.decodeJournal(raw, "", session)
		if err != nil || len(journal.Attempts) == 0 {
			continue
		}
		complete, expired := true, true
		for _, entry := range journal.Attempts {
			complete = complete && entry.Complete
			expired = expired && entry.RetainUntil != nil && !c.cfg.Now().Before(*entry.RetainUntil)
		}
		if !complete {
			continue
		}
		if !expired {
			// Advancing a binding must not erase restart consumption while its
			// original grant window may still be open.
			if journal.AutoUsed {
				continue
			}
			last := journal.Attempts[len(journal.Attempts)-1].Next
			current, err := c.cfg.Runtime.Read(ctx, last)
			if err != nil || c.validateSnapshot(current, current.Writer.Binding) != nil {
				continue // a stale/missing lookup is never advancement proof
			}
			b := current.Writer.Binding
			advanced := true
			for _, entry := range journal.Attempts {
				next := entry.Next
				advanced = advanced && b.TenantID == next.TenantID && b.ProjectID == next.ProjectID &&
					b.OwnedSessionID == next.OwnedSessionID && b.DaemonID == next.DaemonID && sameRun(b.RunID, next.RunID) &&
					b.BindingRevision > next.BindingRevision
			}
			if !advanced {
				continue
			}
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := c.cfg.State.RemoveExact(name, agentsetup.Hash(raw)); err != nil {
			c.poisoned = true // an ambiguous unlink/fsync closes admission too
			return false, err
		}
		return true, nil
	}
	return false, ctx.Err()
}

func (c *Coordinator) save(j decisionJournal) error {
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if len(raw) > journalBytes || len(j.Attempts) > MaxAttempts {
		return fail(nativecontract.ReasonBufferFull)
	}
	if err = c.cfg.State.Write(journalName(j.SessionID), raw, false); err != nil {
		// An ambiguous rename/fsync closes this handle's further admission.
		c.poisoned = true
	}
	return err
}
