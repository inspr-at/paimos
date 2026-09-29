// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

// InstructionSources is the version identity of one context's published merge.
// It carries set ids, versions and digests. It does not carry rule text.
type InstructionSources struct {
	Version  string
	SHA256   string
	ByteSize int
	Sets     []VersionRef
}

// SourcesForContext loads the published merge for a receipt's already-checked
// agent context. The caller compares Version, SHA256 and ByteSize with the
// receipt before storing set versions. A mismatch, or a context that cannot
// merge, is the caller's signal to record only the receipt's own merged hash.
func SourcesForContext(ctx context.Context, tx pgx.Tx, p tenant.Principal, c Context) (InstructionSources, error) {
	var out InstructionSources
	if p.Kind != tenant.Agent || c.TenantID != p.TenantID || c.AgentID != p.ID {
		return out, fail(403, "forbidden", "instruction sources are limited to the receiving agent")
	}
	if err := ValidateContext(c); err != nil {
		return out, err
	}
	owner, err := actorOwner(ctx, tx, p)
	if err != nil || owner != c.PersonID {
		return out, fail(403, "forbidden", "instruction sources are limited to the receiving agent")
	}
	var previous string
	if err = tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&previous); err != nil {
		return out, err
	}
	if err = enterRules(ctx, tx, owner, c.AgentID); err != nil {
		return out, err
	}
	defer func() {
		_, _ = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, previous)
	}()
	sets, err := allSets(ctx, tx, "")
	if err != nil {
		return out, err
	}
	snapshots := make([]Snapshot, 0, len(sets))
	for _, s := range sets {
		if !s.Scope.matches(c) || s.PublishedVersion == "" {
			continue
		}
		snap, err := loadVersion(ctx, tx, s.ID, s.PublishedVersion)
		if err != nil {
			return out, err
		}
		snapshots = append(snapshots, snap)
	}
	merged, err := Merge(c, snapshots, time.Now().UTC())
	if err != nil {
		return out, err
	}
	return InstructionSources{Version: merged.Version, SHA256: merged.SHA256, ByteSize: merged.ByteSize, Sets: append([]VersionRef(nil), merged.Versions...)}, nil
}
