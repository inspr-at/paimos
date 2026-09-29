// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const ClientMaximumHeader = "X-Aeon-Max-Session-File-Bytes"

// Older binaries bound the entire cache at 512 KiB. Reserve space for its URL,
// timestamp and digest wrapper; a small rendered body alone is not sufficient.
const legacyBundleBytes = 512*1024 - 16*1024

func encodedSize(v any) int { raw, _ := json.Marshal(v); return len(raw) }

// ClientReport is request-only, keeping strict session response contracts
// unchanged. Report on every beat: omission must not preserve support after a
// client rollback. A version is diagnostic, never inferred capability evidence.
type ClientReport struct {
	MaxSessionFileBytes *int    `json:"max_session_file_bytes"`
	RulesClientVersion  *string `json:"rules_client_version"`
}

func (c ClientReport) Validate() error {
	if c.MaxSessionFileBytes != nil && (*c.MaxSessionFileBytes < MinBudgetBytes || *c.MaxSessionFileBytes > MaxBytes) {
		return fail(400, "invalid_client_limit", "max_session_file_bytes must be between 2000 and 64000")
	}
	if c.RulesClientVersion != nil && (!line(*c.RulesClientVersion, 80, true) || strings.TrimSpace(*c.RulesClientVersion) != *c.RulesClientVersion) {
		return fail(400, "invalid_client_limit", "rules_client_version must be one line of at most 80 bytes")
	}
	return nil
}

// Reports share one per-tenant advisory lock so heartbeats do not serialize
// on each other. Budget admission takes the same key exclusively: it waits
// for in-flight reports, and new reports wait until that admission commits.
// The key is independent of the tenant access/project-move lock. Budget
// admission never locks session rows.
func lockClientReports(ctx context.Context, tx pgx.Tx) error {
	return lockClientGateMode(ctx, tx, true)
}

func lockClientGate(ctx context.Context, tx pgx.Tx) error {
	return lockClientGateMode(ctx, tx, false)
}

func lockClientGateMode(ctx context.Context, tx pgx.Tx, shared bool) error {
	fn := "pg_advisory_xact_lock"
	if shared {
		fn = "pg_advisory_xact_lock_shared"
	}
	_, err := tx.Exec(ctx, `SELECT `+fn+`(hashtextextended('aeon.rules.clients:' || current_setting('aeon.tenant_id',true),0))`)
	return err
}

// RecordClientReport is called after registration/worker authorization and
// existing session mutations, without changing legacy response contracts.
func RecordClientReport(ctx context.Context, tx pgx.Tx, session string, c ClientReport) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := lockClientReports(ctx, tx); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE harness_sessions SET max_session_file_bytes=$2,rules_client_version=$3,rules_client_seen_at=clock_timestamp() WHERE id=$1`, session, c.MaxSessionFileBytes, c.RulesClientVersion)
	return err
}

type ClientBlocker struct {
	Host    string `json:"host"`
	Harness string `json:"harness"`
	Version string `json:"version,omitempty"`
	Maximum int    `json:"max_session_file_bytes"`
}

// maxBlockingClients bounds the admin inventory. The rest is a count, not a
// second page: the ceiling is still the minimum across every matching session.
const maxBlockingClients = 50

func listedBlockers(all []ClientBlocker) ([]ClientBlocker, int) {
	if len(all) <= maxBlockingClients {
		return all, 0
	}
	return all[:maxBlockingClients], len(all) - maxBlockingClients
}

// clientCeiling runs inside the tenant's rules visibility envelope. Include
// stopped and archived generations: removing a row from the UI isn't an upgrade.
func clientCeiling(ctx context.Context, tx pgx.Tx) (int, []ClientBlocker, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT host,harness,coalesce(rules_client_version,harness_version,''),coalesce(max_session_file_bytes,12000)
		FROM harness_sessions WHERE greatest(created_at,heartbeat_at,rules_client_seen_at) >= transaction_timestamp()-interval '7 days'
		ORDER BY 1,2,3,4`)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	ceiling, count := MaxBytes, 0
	blockers := []ClientBlocker{}
	for rows.Next() {
		var b ClientBlocker
		if err := rows.Scan(&b.Host, &b.Harness, &b.Version, &b.Maximum); err != nil {
			return 0, nil, err
		}
		count++
		ceiling = min(ceiling, b.Maximum)
		if b.Maximum < MaxBytes {
			blockers = append(blockers, b)
		}
	}
	// A report under the legacy default must not make the editor refuse 12,000.
	// putBudget already accepts that default; the published ceiling matches it.
	if count == 0 || ceiling < LegacyMaxBytes {
		ceiling = LegacyMaxBytes
	}
	return ceiling, blockers, rows.Err()
}

func RequestMaximum(r *http.Request) (int, error) {
	values := r.Header.Values(ClientMaximumHeader)
	if len(values) == 0 {
		return LegacyMaxBytes, nil
	}
	if len(values) != 1 {
		return 0, fail(400, "invalid_client_limit", "report one client byte limit")
	}
	n, err := strconv.Atoi(values[0])
	if err != nil || n < MinBudgetBytes || n > MaxBytes {
		return 0, fail(400, "invalid_client_limit", "client byte limit must be between 2000 and 64000")
	}
	return n, nil
}

// MergeForClient enforces the publication budget before adapting delivery. The
// cut retains every locked rule and adds only whole rules, in precedence order.
// Its note lives in the body so even old clients display/cache it unchanged.
func MergeForClient(c Context, snapshots []Snapshot, now time.Time, b Budget, maximum int) (Merged, error) {
	if maximum < MinBudgetBytes || maximum > MaxBytes {
		return Merged{}, fail(400, "invalid_client_limit", "invalid client byte limit")
	}
	m, err := MergeWithin(c, snapshots, now, b)
	legacyEnvelope := maximum <= LegacyMaxBytes
	if err != nil || (m.ByteSize <= maximum && (!legacyEnvelope || encodedSize(m) <= legacyBundleBytes)) {
		return m, err
	}
	// Use the same validated rendering to identify rule precedence; no private
	// layer text or explanation is added to the compatibility notice.
	rendered, err := render(c, snapshots, now, false, nil)
	if err != nil {
		return Merged{}, err
	}
	header := SessionHeader + fmt.Sprintf("> Compatibility cut: this client supports %d bytes with bounded metadata. Some rules are omitted; upgrade the Aeon client for the full session file. All locked rules are retained.\n\n", maximum)
	kept := make([]Rule, 0, len(m.Rules))
	size := len(header)
	for _, r := range m.Rules {
		if r.Strength == "locked" {
			kept = append(kept, r)
			size += len(ruleLine(r))
		}
	}
	if size > maximum {
		return Merged{}, fail(422, "client_floor_too_large", "locked rules cannot fit this client's limit; upgrade the Aeon client before loading rules")
	}
	// Exact JSON cost of the locked baseline; added body lines and rule objects
	// account for escaping before choosing another rule (linear work).
	baseline := m
	baseline.Body, baseline.Rules = header, kept
	for _, r := range kept {
		baseline.Body += ruleLine(r)
	}
	envelope := encodedSize(baseline)
	if legacyEnvelope && envelope > legacyBundleBytes {
		return Merged{}, fail(422, "client_floor_too_large", "locked rules and version metadata exceed this client's cache limit; upgrade the Aeon client before loading rules")
	}
	ordered := slices.Clone(m.Rules)
	slices.SortStableFunc(ordered, func(a, b Rule) int {
		return rendered.from[a.Identity].Scope.rank() - rendered.from[b.Identity].Scope.rank()
	})
	for _, r := range ordered {
		if r.Strength == "locked" {
			continue
		}
		cost := encodedSize(r) + 1 + encodedSize(ruleLine(r)) - 2
		if size+len(ruleLine(r)) <= maximum && (!legacyEnvelope || envelope+cost <= legacyBundleBytes) {
			kept = append(kept, r)
			size += len(ruleLine(r))
			envelope += cost
		}
	}
	slices.SortFunc(kept, func(a, b Rule) int { return strings.Compare(a.Identity, b.Identity) })
	var body strings.Builder
	body.WriteString(header)
	for _, r := range kept {
		body.WriteString(ruleLine(r))
	}
	m.Body, m.Rules = body.String(), kept
	m.ByteSize, m.SHA256 = len(m.Body), digest([]byte(m.Body))
	return m, nil
}
