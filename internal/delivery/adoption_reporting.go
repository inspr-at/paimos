// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"time"
)

// AdoptionReporting is the P3/P4a boundary. P3 owns protected report storage,
// instance rollout configuration and waking the existing automatic job. RequestTx
// runs under the canonical access fences and must compare the job revision; it
// neither applies mappings nor performs provider I/O. ReadReport is authorized
// by P4a and must return a bounded, persisted page, never a fresh dry run.
type AdoptionReporting interface {
	ReadReport(context.Context, tenant.Principal, string, string, int) (AdoptionReport, error)
	RequestTx(context.Context, pgx.Tx, tenant.Principal, string, string, int64) (AdoptionJob, error)
}
type AdoptionReport struct {
	Items             []json.RawMessage `json:"items"`
	NextCursor        string            `json:"next_cursor,omitempty"`
	Incomplete        bool              `json:"incomplete"`
	Revision          int64             `json:"revision"`
	SourceFingerprint string            `json:"source_fingerprint,omitempty"`
}
type AdoptionJob struct {
	State                string          `json:"state"`
	Revision             int64           `json:"revision"`
	Attempts             int             `json:"attempts"`
	ReasonCode           string          `json:"reason_code,omitempty"`
	ReasonMessage        string          `json:"reason_message,omitempty"`
	LastCheckedAt        *time.Time      `json:"last_checked_at"`
	NextAttemptAt        *time.Time      `json:"next_attempt_at"`
	LeaseUntil           *time.Time      `json:"lease_until"`
	ReportRef            string          `json:"report_ref,omitempty"`
	ReportDigest         string          `json:"report_digest,omitempty"`
	SourceFingerprint    string          `json:"source_fingerprint,omitempty"`
	ReportCounts         json.RawMessage `json:"report_counts"`
	ReportIncomplete     bool            `json:"report_incomplete"`
	BackupRef            string          `json:"backup_ref,omitempty"`
	BackupVerifiedAt     *time.Time      `json:"backup_verified_at"`
	CleanupState         string          `json:"cleanup_state"`
	NextReconcileAt      *time.Time      `json:"next_reconcile_at"`
	ReservedBackupBytes  int64           `json:"reserved_backup_bytes"`
	ReservedRestoreSlots int             `json:"reserved_restore_slots"`
}
