// SPDX-License-Identifier: AGPL-3.0-only

// Package deliveryadoption implements the instance-local, one-way release
// migration. Providers are explicit adapters; absent deployment evidence or
// capabilities closes activation. No production backup is performed by tests.
package deliveryadoption

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

const Migration = "releases-v1"
const MaxReportBytes = 12 << 20

// Refusal details are additional to the bounded release/member population.
// A full mapping must still expose every retained offending-row explanation.
const maxReportItems = 200 + 5000 + 5200

var ErrLease = errors.New("adoption lease superseded or expired")
var ErrStale = errors.New("adoption source changed since verified recovery point")
var ErrPrerequisite = errors.New("instance adoption prerequisites are incomplete")

// Authority comes from the instance deployment record, never from a request.
// Authorizer must be an active person. Both principals are rechecked at apply.
type Authority struct {
	Executor   tenant.Principal `json:"executor"`
	Authorizer tenant.Principal `json:"authorizer"`
	Reference  string           `json:"reference"`
}

// Product is an exact tenant/project binding plus immutable binary inputs.
// An empty binding disables product seeding, never applies it to other projects.
type Product struct {
	TenantID         string `json:"tenant_id"`
	ProjectID        string `json:"project_id"`
	Repository       string `json:"repository"`
	HistoryDigest    string `json:"history_digest"`
	VersionDigest    string `json:"version_digest"`
	VersionSequence  int    `json:"version_sequence"`
	HistorySequences []int  `json:"history_sequences"`
}

type Config struct {
	Instance string `json:"instance"`
	Artifact string `json:"artifact"`
	// These are independently verified release evidence, not an opt-in switch.
	RollbackFloor       string      `json:"rollback_floor"`
	PreFirstAdoptionPin string      `json:"pre_first_adoption_pin"`
	ConsumersReady      bool        `json:"consumers_ready"`
	WritersStopped      bool        `json:"writers_stopped"`
	RecoveryReconciled  bool        `json:"recovery_reconciled"`
	Authorities         []Authority `json:"authorities"`
	Product             Product     `json:"product"`
	Quota               Quota       `json:"quota"`
}

type Quota struct {
	BundleBytes    int64 `json:"bundle_bytes"`
	RestoreBytes   int64 `json:"restore_bytes"`
	OperationSlots int   `json:"operation_slots"`
	ActiveBytes    int64 `json:"active_bytes"`
	FailedBytes    int64 `json:"failed_bytes"`
	ProtectedBytes int64 `json:"protected_bytes"`
}

type Capabilities struct {
	DurableLookup            bool `json:"durable_lookup"`
	Idempotency              bool `json:"idempotency"`
	Cancellation             bool `json:"cancellation"`
	HardExpiry               bool `json:"hard_expiry"`
	QuotaSeparation          bool `json:"quota_separation"`
	RecoveryCatalog          bool `json:"recovery_catalog"`
	ReferenceAwareRetirement bool `json:"reference_aware_retirement"`
	IsolatedRestore          bool `json:"isolated_restore"`
}

type Identity struct {
	Instance   string `json:"instance"`
	Tenant     string `json:"tenant"`
	Project    string `json:"project"`
	Migration  string `json:"migration"`
	Attempt    string `json:"attempt"`
	Generation int64  `json:"generation"`
}

// Operation is the closed P1 journal shape. Stable keys predate every effect;
// old generations retain the original identity until reclamation is confirmed.
type Operation struct {
	Key       string     `json:"operation_key"`
	Attempt   string     `json:"attempt_id"`
	Kind      string     `json:"kind"`
	Status    string     `json:"status"`
	Handle    string     `json:"provider_handle,omitempty"`
	Identity  Identity   `json:"ownership_labels"`
	Bytes     int64      `json:"reserved_bytes,omitempty"`
	Slots     int        `json:"reserved_restore_slots,omitempty"`
	Deadline  time.Time  `json:"deadline"`
	Cleanup   string     `json:"cleanup_state,omitempty"`
	NextCheck *time.Time `json:"next_check_at,omitempty"`
	Pin       string     `json:"recovery_pin_manifest_ref,omitempty"`
}
type Journal struct {
	Operations []Operation `json:"operations"`
}

type ProviderRequest struct {
	Operation     Operation `json:"operation"`
	Fingerprint   string    `json:"fingerprint"`
	Backup        string    `json:"backup,omitempty"`
	Pin           string    `json:"pin,omitempty"`
	Quota         Quota     `json:"quota"`
	RestoreExpiry time.Time `json:"restore_expiry"`
	PayloadExpiry time.Time `json:"payload_expiry"`
}

// ProviderResult is bound evidence, including an actual successful isolated
// restore and the protected complete base/WAL dependency closure. A checksum,
// scheduled backup or provider handle alone is deliberately insufficient.
type ProviderResult struct {
	Key               string   `json:"operation_key"`
	Kind              string   `json:"kind"`
	Identity          Identity `json:"identity"`
	State             string   `json:"state"` // complete, pending, missing, reclaimed
	Handle            string   `json:"handle"`
	BackupRef         string   `json:"backup_ref"`
	BackupDigest      string   `json:"backup_digest"`
	RestoreRef        string   `json:"restore_ref"`
	RestoreDigest     string   `json:"restore_digest"`
	Fingerprint       string   `json:"fingerprint"`
	IntegrityVerified bool     `json:"integrity_verified"`
	ChainVerified     bool     `json:"chain_verified"`
	Restored          bool     `json:"restored"`
	Pin               string   `json:"pin"`
	Protected         bool     `json:"protected"`
	Unadopted         bool     `json:"unadopted"`
	Bytes             int64    `json:"bytes"`
}

type CatalogPage struct {
	Items []ProviderResult `json:"items"`
	Next  string           `json:"next_cursor"`
}

// Provider must durably enforce the supplied quota and hard expiries, support
// lookup even when no handle was returned, and keep recovery pins independently
// of this database. Cleanup is reference-aware; it never retires a successful
// pin. List is provider-native keyset paging within this instance's namespace.
type Provider interface {
	Capabilities(context.Context) (Capabilities, error)
	Lookup(context.Context, Operation) (ProviderResult, error)
	Execute(context.Context, ProviderRequest) (ProviderResult, error)
	List(context.Context, string, string, int) (CatalogPage, error)
}

// Reports is protected instance-local storage, with a per-project current and
// latest-failed report bound. References do not grant backup/download access.
type Reports interface {
	Put(context.Context, Identity, []byte) (string, error)
	Get(context.Context, string) ([]byte, error)
	RetainFailed(context.Context, string) (string, error)
}

type Reason struct {
	Code   string `json:"code"`
	NodeID string `json:"node_id,omitempty"`
	Fix    string `json:"fix"`
}
type Counts struct {
	Releases int  `json:"releases"`
	Members  int  `json:"members"`
	Active   int  `json:"active"`
	AtLeast  bool `json:"at_least"`
}
type ReleaseMapping struct {
	ID               string     `json:"id"`
	Sequence         int        `json:"sequence"`
	OriginalSequence int        `json:"original_sequence"`
	State            string     `json:"state"`
	Origin           string     `json:"origin"`
	Rank             string     `json:"rank"`
	Scheme           string     `json:"scheme,omitempty"`
	Version          string     `json:"version,omitempty"`
	ReleasedAt       *time.Time `json:"released_at,omitempty"`
	CompletionBasis  string     `json:"completion_basis,omitempty"`
	DefaultTitle     bool       `json:"default_title"`
}
type MemberMapping struct {
	ID      string `json:"id"`
	Release string `json:"release,omitempty"`
	Rank    string `json:"rank"`
	Source  string `json:"source"`
	Deleted bool   `json:"deleted"`
}
type Report struct {
	Identity     Identity         `json:"identity"`
	Fingerprint  string           `json:"fingerprint"`
	CheckedAt    time.Time        `json:"checked_at"`
	Eligible     bool             `json:"eligible"`
	Incomplete   bool             `json:"incomplete"`
	Counts       Counts           `json:"counts"`
	NextSequence int              `json:"next_sequence"`
	Releases     []ReleaseMapping `json:"releases"`
	Members      []MemberMapping  `json:"members"`
	Reasons      []Reason         `json:"reasons"`
}

type Status struct {
	Project          string     `json:"project_node_id"`
	Mode             string     `json:"mode"`
	State            string     `json:"state"`
	Revision         int64      `json:"revision"`
	Attempts         int        `json:"attempts"`
	ReasonCode       string     `json:"reason_code,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	ReportRef        string     `json:"report_ref,omitempty"`
	ReportDigest     string     `json:"report_digest,omitempty"`
	Incomplete       bool       `json:"incomplete"`
	Counts           Counts     `json:"counts"`
	LastCheck        *time.Time `json:"last_checked_at"`
	BackupVerifiedAt *time.Time `json:"backup_verified_at"`
	BackupRef        string     `json:"backup_ref,omitempty"`
	Cleanup          string     `json:"cleanup_state"`
	NextCheck        *time.Time `json:"next_reconcile_at"`
	NextAttempt      time.Time  `json:"next_attempt_at"`
}
