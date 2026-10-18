// SPDX-License-Identifier: AGPL-3.0-only

// Package nativereceipts journals native effects before I/O and retains only
// explicitly approved final answers. It does not own or resume a process.
package nativereceipts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

const (
	MaxReceipts      = 512
	MaxPendingInputs = 32
	MaxFinals        = 64
	MaxFinalBytes    = 4 << 20
	MaxTextBytes     = 65536
	MaxRetention     = 24 * time.Hour
)

// Error never contains content, native protocol bodies or authority tokens.
type Error struct{ Reason nativecontract.Reason }

func (e *Error) Error() string                { return string(e.Reason) }
func Fail(reason nativecontract.Reason) error { return &Error{Reason: reason} }

// Intent contains metadata only. PayloadSHA256 is computed from the validated
// command in memory; the command body stays in its authorized inbox/chat store.
type Intent struct {
	Receipt               nativecontract.Receipt   `json:"receipt"`
	ActorID               string                   `json:"actor_id"`
	PayloadSHA256         string                   `json:"payload_sha256"`
	ExpiresAt             time.Time                `json:"expires_at"`
	ProcessIdentityDigest string                   `json:"process_identity_digest"`
	EffectDigest          string                   `json:"effect_digest"`
	Reports               []nativecontract.Receipt `json:"reports"`
	ReportOffset          int                      `json:"report_offset"`
	ReportAttempts        uint32                   `json:"report_attempts"`
}

// Decode the reports array separately so its 16-item bound precedes allocation
// of receipt structs even in a corrupt private snapshot. The outer decoder
// separately bounds operation count and file bytes.
func (in *Intent) UnmarshalJSON(raw []byte) error {
	type metadata Intent
	var value metadata
	fields := struct {
		*metadata
		Reports json.RawMessage `json:"reports"`
	}{metadata: &value}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&fields); err != nil {
		return Fail(nativecontract.ReasonInvalidInput)
	}
	reports, err := readArray[nativecontract.Receipt](fields.Reports, 16)
	if err != nil {
		return err
	}
	value.Reports = reports
	*in = Intent(value)
	return nil
}

func Digest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", Fail(nativecontract.ReasonInvalidInput)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func ValidDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func Validate(v *nativecontract.Validator, schema string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return Fail(nativecontract.ReasonInvalidInput)
	}
	return v.ValidateJSON(schema, raw)
}

func cloneReceipt(r nativecontract.Receipt) nativecontract.Receipt {
	if r.Binding != nil {
		b := cloneBinding(*r.Binding)
		r.Binding = &b
	}
	if r.InputID != nil {
		s := *r.InputID
		r.InputID = &s
	}
	if r.TurnRef != nil {
		s := *r.TurnRef
		r.TurnRef = &s
	}
	if r.Reason != nil {
		s := *r.Reason
		r.Reason = &s
	}
	return r
}

func cloneBinding(b nativecontract.Binding) nativecontract.Binding {
	if b.RunID != nil {
		s := *b.RunID
		b.RunID = &s
	}
	return b
}

func SameIntent(a, b Intent) bool {
	return a.Receipt.RequestID == b.Receipt.RequestID && a.Receipt.OperationID == b.Receipt.OperationID &&
		a.Receipt.Operation == b.Receipt.Operation && a.Receipt.Binding != nil && b.Receipt.Binding != nil &&
		a.Receipt.Binding.Equal(*b.Receipt.Binding) && sameString(a.Receipt.InputID, b.Receipt.InputID) &&
		a.ActorID == b.ActorID && a.PayloadSHA256 == b.PayloadSHA256 && a.ExpiresAt.Equal(b.ExpiresAt) &&
		a.ProcessIdentityDigest == b.ProcessIdentityDigest && a.EffectDigest == b.EffectDigest
}

func sameString(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// Transition rejects application acceptance as native evidence. Inputs cannot
// skip the possible-write marker, controls need applied, and stop needs exit.
// A qualified late receipt can resolve an uncertain write; it cannot resend it.
func Transition(before, after nativecontract.Receipt) error {
	if before.RequestID != after.RequestID || before.OperationID != after.OperationID || before.Operation != after.Operation ||
		before.Binding == nil || after.Binding == nil || !before.Binding.Equal(*after.Binding) ||
		!sameString(before.InputID, after.InputID) || before.TurnRef != nil && !sameString(before.TurnRef, after.TurnRef) {
		return Fail(nativecontract.ReasonIdentityMismatch)
	}
	oldSeq, err := strconv.ParseUint(before.SourceSequence, 10, 64)
	if err != nil {
		return Fail(nativecontract.ReasonInvalidInput)
	}
	newSeq, err := strconv.ParseUint(after.SourceSequence, 10, 64)
	if err != nil {
		return Fail(nativecontract.ReasonInvalidInput)
	}
	if before.State == after.State {
		if oldSeq != newSeq || !sameString(before.TurnRef, after.TurnRef) || !sameReason(before.Reason, after.Reason) {
			return Fail(nativecontract.ReasonIdentityMismatch)
		}
		return nil
	}
	if before.State == "rejected" || before.State == "expired" || before.State == "turn_completed" || before.State == "applied" || before.State == "exit_confirmed" {
		return Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	possible := before.State != "accepted" && before.State != "queued"
	switch after.State {
	case "queued":
		if before.State != "accepted" {
			return Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	case "write_started":
		if possible {
			return Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	case "uncertain":
		if !possible || after.Reason == nil {
			return Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	case "rejected", "expired":
		if possible || after.Reason == nil {
			return Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	case "write_acknowledged":
		if before.State != "write_started" && before.State != "uncertain" {
			return Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	case "turn_started":
		if before.Operation != "input" || before.State != "write_acknowledged" || after.InputID == nil || after.TurnRef == nil {
			return Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	case "turn_completed":
		if before.Operation != "input" || before.State != "turn_started" || after.TurnRef == nil {
			return Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	case "applied":
		if before.Operation == "input" || before.Operation == "stop" || !possible {
			return Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	case "exit_confirmed":
		if before.Operation != "stop" || !possible {
			return Fail(nativecontract.ReasonOwnershipUnconfirmed)
		}
	default:
		return Fail(nativecontract.ReasonInvalidInput)
	}
	provider := after.State == "write_acknowledged" || after.State == "turn_started" || after.State == "turn_completed" || after.State == "applied" || after.State == "exit_confirmed"
	if provider && (newSeq == 0 || newSeq <= oldSeq) {
		return Fail(nativecontract.ReasonIdentityMismatch)
	}
	if !provider && newSeq != oldSeq {
		return Fail(nativecontract.ReasonIdentityMismatch)
	}
	if provider && after.Reason != nil {
		return Fail(nativecontract.ReasonInvalidInput)
	}
	return nil
}

func sameReason(a, b *nativecontract.Reason) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
