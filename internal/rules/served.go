// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/workorders"
)

// MaxApplicableSets is the most sets one served merge can name. The publication
// budget counts every set as at least one rule and refuses a store above
// maxBudgetRules, so a manifest cannot list more.
func MaxApplicableSets() int { return maxBudgetRules }

// absentSelector stands in for an empty agent or task so the served-manifest
// identity is comparable. It is the same placeholder the budget check uses for
// "no such project"; it is not a principal.
const absentSelector = "00000000-0000-4000-8000-000000000000"

func selectorID(id string) string {
	if id == "" {
		return absentSelector
	}
	return id
}

// RecordServedManifest stores the constituent set identities of one successful
// merge response. The row is the manifest that was actually served: set id,
// version and snapshot digest only, never rule text. Repeating the same serve
// does not add a row. A later publication that renders the same bytes with a
// different set list is a second row, which leaves attribution unproven.
func RecordServedManifest(ctx context.Context, tx pgx.Tx, c Context, m Merged) error {
	if err := ValidateContext(c); err != nil {
		return err
	}
	if len(m.Versions) == 0 {
		return nil
	}
	if len(m.Versions) > MaxApplicableSets() {
		return fail(422, "rules_budget_exceeded", "served rule sets exceed the publication budget")
	}
	sets := append([]VersionRef(nil), m.Versions...)
	slices.SortFunc(sets, func(a, b VersionRef) int { return strings.Compare(a.SetID, b.SetID) })
	for _, set := range sets {
		if !workorders.UUID(set.SetID) || strings.ToLower(set.SetID) != set.SetID || !releasehistory.ValidVersion(set.Version) || !hex64(set.SHA256) {
			return fail(409, "snapshot_integrity", "served manifest is missing a set version")
		}
	}
	raw, err := json.Marshal(sets)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	_, err = tx.Exec(ctx, `INSERT INTO rule_served_manifests(
		tenant_id, project_id, person_id, agent_id, task_id, role, harness, version, body_sha256, byte_size, sets_digest, sets)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb)
		ON CONFLICT ON CONSTRAINT rule_served_manifests_identity DO NOTHING`,
		c.TenantID, c.ProjectID, c.PersonID, selectorID(c.AgentID), selectorID(c.TaskID), c.Role, c.Harness, m.Version, m.SHA256, m.ByteSize, sum[:], string(raw))
	return err
}

// ServedSetVersions returns the constituent sets of the one manifest served for
// this context and these rendered bytes. proven is false when none was recorded
// or when more than one set list was served for the same bytes: the merged
// version is only the maximum constituent version, so a matching body is not
// proof of the current sets. The caller then records the merged identity alone.
func ServedSetVersions(ctx context.Context, tx pgx.Tx, c Context, version, bodySHA256 string, byteSize int) ([]VersionRef, bool, error) {
	if err := ValidateContext(c); err != nil {
		return nil, false, err
	}
	rows, err := tx.Query(ctx, `SELECT sets FROM rule_served_manifests
		WHERE project_id=$1 AND person_id=$2 AND agent_id=$3 AND task_id=$4
		  AND role=$5 AND harness=$6 AND version=$7 AND body_sha256=$8 AND byte_size=$9`,
		c.ProjectID, c.PersonID, selectorID(c.AgentID), selectorID(c.TaskID), c.Role, c.Harness, version, bodySHA256, byteSize)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var lists [][]VersionRef
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, false, err
		}
		var sets []VersionRef
		if err = json.Unmarshal(raw, &sets); err != nil {
			return nil, false, err
		}
		lists = append(lists, sets)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	if len(lists) != 1 {
		return nil, false, nil
	}
	return lists[0], true, nil
}

func hex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if r < '0' || (r > '9' && r < 'a') || r > 'f' {
			return false
		}
	}
	return true
}
