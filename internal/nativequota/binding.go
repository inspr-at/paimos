// SPDX-License-Identifier: AGPL-3.0-only

// Package nativequota normalizes quota facts from an already owned native
// connection. It never launches a probe or handles vendor authentication.
package nativequota

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

const (
	ClaudeSchema = "claude-rate-limit/1"
	CodexSchema  = "codex-rate-limits/1"
	FreshFor     = 10 * time.Minute
	MaxWindows   = 32
)

var (
	ErrAccount       = errors.New("quota account mismatch")
	ErrRevision      = errors.New("quota profile or enrollment revision mismatch")
	ErrRuntime       = errors.New("quota owned connection mismatch")
	ErrQualification = errors.New("quota stream pin is not qualified")
	ErrProtocol      = errors.New("invalid quota event")
	ErrBounds        = errors.New("quota event exceeds bounds")
	ErrSequence      = errors.New("quota event is not newer")
	ErrNoReadings    = errors.New("no fresh quota readings")
	ErrScope         = errors.New("quota scope is not qualified")
)

type Clock interface{ Now() time.Time }

// Binding comes from the owning runtime, never the vendor frame or browser.
// AccountBindingRevision is the existing enrollment's link_revision; it is
// distinct from the immutable local ProfileBindingRevision.
type Binding struct {
	Runtime                                        nativecontract.Binding
	Identity                                       nativecontract.NativeIdentity
	AccountID, Harness                             string
	AccountBindingRevision, ProfileBindingRevision int64
	Binary, SDK                                    nativecontract.BinaryPin
}

// Qualification is supplied ONLY by the integrator's release-owned compiled
// qualification registry. Matching metadata is not itself qualification or
// permission to start a runtime. There is no config/JSON qualification loader.
// CodexBuckets allowlists vendor limit IDs; arbitrary vendor labels are private.
// SDK is required for Claude's SDK stream and empty for the Codex app-server.
type Qualification struct {
	Harness, Schema, EvidenceDigest string
	Binary, SDK                     nativecontract.BinaryPin
	CodexBuckets                    []string
}

var (
	digestRE = regexp.MustCompile(`^[a-f0-9]{64}$`)
	bucketRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$`)
)

func pinValid(p nativecontract.BinaryPin) bool {
	return p.Path != "" && len(p.Path) <= 4096 && p.Version != "" && len(p.Version) <= 128 &&
		p.Protocol != "" && len(p.Protocol) <= 128 && digestRE.MatchString(p.SHA256)
}

func (b Binding) valid() bool {
	// Validate the frozen wire binding, including UUIDs and safe revisions.
	v, err := nativecontract.NewValidator()
	raw, marshalErr := json.Marshal(b.Runtime)
	if err != nil || marshalErr != nil || v.ValidateJSON("NativeBinding", raw) != nil {
		return false
	}
	return uuidRE.MatchString(b.AccountID) && b.AccountBindingRevision >= 0 && b.AccountBindingRevision <= 9007199254740991 &&
		b.ProfileBindingRevision >= 0 && b.ProfileBindingRevision <= 9007199254740991 &&
		(b.Harness == "claude" || b.Harness == "codex") && b.Identity.Provider == b.Harness &&
		b.Identity.RuntimeEpoch == b.Runtime.RuntimeEpoch && !b.Identity.StartedAt.IsZero() && digestRE.MatchString(b.Identity.ProcessIdentityDigest) &&
		(b.Harness == "claude" && b.Identity.NativeSessionRef != "" && len(b.Identity.NativeSessionRef) <= 256 ||
			b.Harness == "codex" && b.Identity.NativeThreadRef != "" && len(b.Identity.NativeThreadRef) <= 256) && pinValid(b.Binary)
}

var uuidRE = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func (b Binding) check(other Binding) error {
	if b.AccountID != other.AccountID || b.Runtime.TenantID != other.Runtime.TenantID {
		return ErrAccount
	}
	if b.AccountBindingRevision != other.AccountBindingRevision || b.ProfileBindingRevision != other.ProfileBindingRevision {
		return ErrRevision
	}
	if !b.Runtime.Equal(other.Runtime) || !sameConnection(b.Identity, other.Identity) || b.Harness != other.Harness {
		return ErrRuntime
	}
	if b.Binary != other.Binary || b.SDK != other.SDK {
		return ErrQualification
	}
	return nil
}

// Turn/cursor refs are mutable correlation data on a persistent owned stream.
// They neither change the account quota scope nor confer runtime authority.
func sameConnection(a, b nativecontract.NativeIdentity) bool {
	return a.Provider == b.Provider && a.RuntimeEpoch == b.RuntimeEpoch && a.NativeSessionRef == b.NativeSessionRef &&
		a.NativeThreadRef == b.NativeThreadRef && a.ProcessIdentityDigest == b.ProcessIdentityDigest && a.PID == b.PID && a.StartedAt.Equal(b.StartedAt)
}

func qualified(b Binding, q Qualification) bool {
	if !digestRE.MatchString(q.EvidenceDigest) || q.Harness != b.Harness || q.Binary != b.Binary || !pinValid(q.Binary) {
		return false
	}
	if b.Harness == "claude" {
		return q.Schema == ClaudeSchema && q.SDK == b.SDK && pinValid(q.SDK) && len(q.CodexBuckets) == 0
	}
	if q.Schema != CodexSchema || q.SDK != (nativecontract.BinaryPin{}) || b.SDK != q.SDK || len(q.CodexBuckets) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, bucket := range q.CodexBuckets {
		if !bucketRE.MatchString(bucket) || seen[bucket] {
			return false
		}
		seen[bucket] = true
	}
	return true
}

// Fact contains no account names, paths or vendor payloads. Sign-in and binary
// facts use ready/blocked/unknown; only an identity/pin check changes them.
type Fact struct {
	State      string
	ObservedAt time.Time
}

type Slots struct {
	Fact
	Used, Limit int
}

type Readiness struct {
	SignIn, BinaryPin       Fact
	HostSlots, AccountSlots Slots
}

type Allowance struct {
	Fact
	ResetsAt *time.Time
}

func (r Readiness) valid(now time.Time) bool {
	for _, f := range []Fact{r.SignIn, r.BinaryPin, r.HostSlots.Fact, r.AccountSlots.Fact} {
		if f.State != "ready" && f.State != "blocked" && f.State != "unknown" {
			return false
		}
		if f.ObservedAt.IsZero() || f.ObservedAt.After(now) {
			return false
		}
	}
	for _, slots := range []Slots{r.HostSlots, r.AccountSlots} {
		if slots.Used < 0 || slots.Used > 1000000 || slots.Limit < 0 || slots.Limit > 1000000 {
			return false
		}
	}
	return true
}

// Snapshot is a local diagnostic value. Public output uses only Quota, after
// the existing owner/accountprivacy and native-session visibility checks.
type Snapshot struct {
	Quota       nativecontract.Quota
	Measurement string    // missing, fresh, stale, failed, unsupported
	Failure     string    // bounded check category; never an error body
	Allowance   Allowance // allowed, limited, unknown; independent of utilization
	Readiness   Readiness
}
