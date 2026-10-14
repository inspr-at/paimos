// SPDX-License-Identifier: AGPL-3.0-only

package nativerecovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

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
}

type decisionJournal struct {
	Version   int       `json:"version"`
	TenantID  string    `json:"tenant_id"`
	SessionID string    `json:"owned_session_id"`
	AutoUsed  bool      `json:"auto_used"`
	Attempts  []attempt `json:"attempts"`
}

func journalName(session string) string { return "recovery-" + session + ".json" }

func (c *Coordinator) read(b nativecontract.Binding) (decisionJournal, error) {
	out := decisionJournal{Version: 1, TenantID: b.TenantID, SessionID: b.OwnedSessionID, Attempts: []attempt{}}
	raw, err := c.cfg.State.Read(journalName(b.OwnedSessionID), journalBytes)
	if errors.Is(err, os.ErrNotExist) {
		names, err := c.cfg.State.Names(MaxSessions + 1)
		if err != nil {
			return out, err
		}
		// One recovery.lock plus at most 128 conversation decision snapshots.
		if len(names) >= MaxSessions+1 {
			return out, fail(nativecontract.ReasonBufferFull)
		}
		return out, nil
	}
	if err != nil {
		return out, err
	}
	// Inspect the bounded array as raw bytes before allocating attempts. The
	// file cap and item count both precede decoding local metadata structs.
	var header struct {
		Version   int             `json:"version"`
		TenantID  string          `json:"tenant_id"`
		SessionID string          `json:"owned_session_id"`
		AutoUsed  bool            `json:"auto_used"`
		Attempts  json.RawMessage `json:"attempts"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&header) != nil || d.Decode(new(any)) != io.EOF || header.Version != 1 ||
		header.TenantID != b.TenantID || header.SessionID != b.OwnedSessionID {
		return out, fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
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
			entry.Previous.TenantID != b.TenantID || entry.Next.TenantID != b.TenantID ||
			entry.Previous.OwnedSessionID != b.OwnedSessionID || entry.Next.OwnedSessionID != b.OwnedSessionID ||
			entry.Previous.ProjectID != entry.Next.ProjectID || entry.Previous.RuntimeEpoch == entry.Next.RuntimeEpoch ||
			entry.Previous.HarnessSessionID == entry.Next.HarnessSessionID || entry.Next.BindingRevision <= entry.Previous.BindingRevision {
			return out, fail(nativecontract.ReasonOwnershipUnconfirmed)
		}
		ids[entry.RequestID] = true
		out.Attempts = append(out.Attempts, entry)
	}
	if token, err := d.Token(); err != nil || token != json.Delim(']') || d.Decode(new(any)) != io.EOF {
		return out, fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	out.AutoUsed = header.AutoUsed
	return out, nil
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
