// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// This allowlist contains mutable model references only. Account grants, caps,
// running work and immutable model pins cannot be rewritten by retirement.
type retireTable struct {
	name     string
	keys     []string
	revision bool
}

var retireTables = []retireTable{
	{"model_pref_orders", []string{"profile_id", "column_key", "situation"}, false},
	{"model_pref_situation_orders", []string{"profile_id", "column_key", "situation"}, false},
	{"model_row_constraints", []string{"scope", "project_id", "column_key", "line"}, false},
	{"model_pauses", []string{"harness", "model"}, false},
	{"model_pref_profiles", []string{"id"}, true},
	{"model_rules", []string{"scope", "project_id", "column_key", "line"}, false},
	{"model_rule_revisions", []string{"scope", "scope_key"}, true},
	{"model_role_routes", []string{"role", "priority"}, false},
	{"model_security_role_routes", []string{"role", "priority"}, false},
	{"model_pref_cells", []string{"scope_id", "kind_id", "bucket"}, false},
	{"model_pref_scopes", []string{"id"}, true},
	{"model_profile_retirements", []string{"profile_id"}, false},
}

type retireSnapshot map[string][]json.RawMessage

func loadRetireSnapshot(ctx context.Context, tx pgx.Tx) (retireSnapshot, error) {
	out := retireSnapshot{}
	budget := 4 << 20
	for _, table := range retireTables {
		rows, err := tx.Query(ctx, `SELECT CASE WHEN octet_length(to_jsonb(t)::text)<=16384 THEN to_jsonb(t) END FROM `+table.name+` t ORDER BY `+strings.Join(table.keys, ",")+` LIMIT 2049`)
		if err != nil {
			return nil, err
		}
		out[table.name] = []json.RawMessage{}
		for rows.Next() {
			var raw json.RawMessage
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, err
			}
			budget -= len(raw)
			if len(raw) == 0 || budget < 0 || len(out[table.name]) >= 2048 {
				rows.Close()
				return nil, fail(413, "retirement reference limit")
			}
			out[table.name] = append(out[table.name], raw)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func retireDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func rowFields(raw json.RawMessage) map[string]json.RawMessage {
	var row map[string]json.RawMessage
	_ = json.Unmarshal(raw, &row)
	return row
}
func rowString(row map[string]json.RawMessage, key string) string { return rawString(row[key]) }
func rowKey(table retireTable, raw json.RawMessage) string {
	fields := rowFields(raw)
	parts := []json.RawMessage{}
	for _, key := range table.keys {
		parts = append(parts, fields[key])
	}
	b, _ := json.Marshal(parts)
	return string(b)
}
func snapshotRows(table retireTable, rows []json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, row := range rows {
		out[rowKey(table, row)] = row
	}
	return out
}

// Events retain only changed references, rather than unrelated tenant choices.
func changedRetireSnapshots(before, after retireSnapshot) (retireSnapshot, retireSnapshot) {
	b, a := retireSnapshot{}, retireSnapshot{}
	for _, table := range retireTables {
		old, next := snapshotRows(table, before[table.name]), snapshotRows(table, after[table.name])
		for key, row := range old {
			if !bytes.Equal(row, next[key]) {
				b[table.name] = append(b[table.name], row)
			}
		}
		for key, row := range next {
			if !bytes.Equal(row, old[key]) {
				a[table.name] = append(a[table.name], row)
			}
		}
		for _, s := range []retireSnapshot{b, a} {
			sort.Slice(s[table.name], func(i, j int) bool { return rowKey(table, s[table.name][i]) < rowKey(table, s[table.name][j]) })
		}
	}
	return b, a
}
func undoRetireSnapshots(ctx context.Context, tx pgx.Tx, before, after, current retireSnapshot) error {
	// Detect both edits and collisions with a restored key before any mutation.
	for _, table := range retireTables {
		actual, old, next := snapshotRows(table, current[table.name]), snapshotRows(table, before[table.name]), snapshotRows(table, after[table.name])
		for key, row := range next {
			if !bytes.Equal(actual[key], row) {
				return fail(409, "retirement references changed")
			}
		}
		for key := range old {
			if _, existed := next[key]; !existed && actual[key] != nil {
				return fail(409, "retirement references changed")
			}
		}
	}
	for _, table := range retireTables {
		b, a := before[table.name], after[table.name]
		if len(b)+len(a) == 0 {
			continue
		}
		matches := []string{}
		for _, key := range table.keys {
			matches = append(matches, "t."+key+" IS NOT DISTINCT FROM s."+key)
		}
		join := strings.Join(matches, " AND ")
		if table.revision {
			// Revision tokens are monotonic even when content is restored.
			raw, _ := json.Marshal(b)
			if _, err := tx.Exec(ctx, `UPDATE `+table.name+` t SET revision=t.revision+1 FROM jsonb_populate_recordset(NULL::`+table.name+`,$1::jsonb) s WHERE `+join, string(raw)); err != nil {
				return err
			}
			continue
		}
		if len(a) > 0 {
			raw, _ := json.Marshal(a)
			if _, err := tx.Exec(ctx, `DELETE FROM `+table.name+` t USING jsonb_populate_recordset(NULL::`+table.name+`,$1::jsonb) s WHERE `+join, string(raw)); err != nil {
				return err
			}
		}
		if len(b) > 0 {
			raw, _ := json.Marshal(b)
			if _, err := tx.Exec(ctx, `INSERT INTO `+table.name+` SELECT * FROM jsonb_populate_recordset(NULL::`+table.name+`,$1::jsonb)`, string(raw)); err != nil {
				return err
			}
		}
	}
	return nil
}

type retireUse struct {
	Label    string  `json:"label"`
	Who      string  `json:"who"`
	From     string  `json:"from"`
	To       *string `json:"to"`
	project  string
	personal string
}

func retirementLines(model lifecycleModel, all []lifecycleModel) map[string]bool {
	lines := map[string]bool{}
	for _, p := range model.Profiles {
		if p.Retired {
			continue
		}
		lines[boardLineID(p)] = true
		line, _ := modelsLine(p)
		lines[line] = true
	}
	// Stable line references already choose a newer active version automatically.
	// They move only if the selected version is still the active line head.
	for _, next := range all {
		if next.Identity == model.Identity {
			continue
		}
		for _, p := range next.Profiles {
			if !p.Retired && p.Enabled && compareCatalogVersions(next.Version, model.Version) > 0 {
				delete(lines, boardLineID(p))
				line, _ := modelsLine(p)
				delete(lines, line)
			}
		}
	}
	return lines
}

func retirementUses(model lifecycleModel, s retireSnapshot, all []lifecycleModel) []retireUse {
	out := []retireUse{}
	ids := map[string]bool{}
	for _, p := range model.Profiles {
		if !p.Retired {
			ids[p.ID] = true
		}
	}
	lines := retirementLines(model, all)
	prefs := map[string]map[string]json.RawMessage{}
	for _, raw := range s["model_pref_profiles"] {
		row := rowFields(raw)
		prefs[rowString(row, "id")] = row
	}
	scopes := map[string]map[string]json.RawMessage{}
	for _, raw := range s["model_pref_scopes"] {
		row := rowFields(raw)
		scopes[rowString(row, "id")] = row
	}
	add := func(label, who, project, person string) {
		if len(out) > 2048 {
			return
		}
		out = append(out, retireUse{Label: label, Who: who, From: model.Identity, To: model.Lifecycle.Successor, project: project, personal: person})
	}
	for _, raw := range append(append([]json.RawMessage{}, s["model_pref_orders"]...), s["model_pref_situation_orders"]...) {
		row := rowFields(raw)
		pref := prefs[rowString(row, "profile_id")]
		person := rowString(pref, "person_id")
		who := "Default"
		if person != "" {
			who = person
		}
		var rank, not []string
		_ = json.Unmarshal(row["rank"], &rank)
		_ = json.Unmarshal(row["not_allowed"], &not)
		for i, line := range rank {
			if lines[line] {
				label := "fallback"
				if i == 0 {
					label = "Default"
					if rowString(row, "column_key") != "other" {
						label = "Except for"
					}
				}
				add(label+" · "+rowString(row, "column_key")+" · "+rowString(row, "situation"), who, rowString(pref, "project_id"), person)
			}
		}
		for _, line := range not {
			if lines[line] {
				add("never · "+rowString(row, "column_key"), who, rowString(pref, "project_id"), person)
			}
		}
	}
	for _, raw := range s["model_rules"] {
		row := rowFields(raw)
		if lines[rowString(row, "line")] && string(row["moved_to_models"]) != "true" {
			add("lock · "+rowString(row, "lock")+" · "+rowString(row, "column_key"), rowString(row, "set_by"), rowString(row, "project_id"), "")
		}
	}
	for _, raw := range s["model_row_constraints"] {
		row := rowFields(raw)
		if lines[rowString(row, "line")] {
			add("lock · "+rowString(row, "kind")+" · "+rowString(row, "column_key"), rowString(row, "set_by"), rowString(row, "project_id"), "")
		}
	}
	for _, raw := range s["model_pauses"] {
		row := rowFields(raw)
		for _, p := range model.Profiles {
			if !p.Retired && p.Harness == rowString(row, "harness") && p.Model == rowString(row, "model") {
				add("pause · "+p.Harness, rowString(row, "set_by"), "", "")
				break
			}
		}
	}
	for _, table := range []string{"model_role_routes", "model_security_role_routes"} {
		for _, raw := range s[table] {
			row := rowFields(raw)
			if ids[rowString(row, "profile_id")] {
				add("fallback · "+rowString(row, "role"), "Default", "", "")
			}
		}
	}
	for _, raw := range s["model_pref_cells"] {
		row := rowFields(raw)
		if ids[rowString(row, "profile_id")] {
			scope := scopes[rowString(row, "scope_id")]
			person, project := rowString(scope, "person_id"), rowString(scope, "project_id")
			who := "Default"
			if person != "" {
				who = person
			}
			add("pinned · "+rowString(row, "bucket"), who, project, person)
		}
	}
	return out
}
func visibleRetireUses(ctx context.Context, tx pgx.Tx, p tenant.Principal, uses []retireUse) ([]retireUse, bool, error) {
	manager := authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{}) == nil
	person, err := currentPreferencePerson(ctx, tx, p)
	if err != nil {
		return nil, false, err
	}
	out := []retireUse{}
	incomplete := false
	for _, use := range uses {
		if !manager && (use.personal != "" && (person == nil || *person != use.personal) || use.project != "") {
			incomplete = true
			continue
		}
		if use.project != "" && authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: use.project}) != nil {
			incomplete = true
			continue
		}
		out = append(out, use)
	}
	return out, incomplete, nil
}

// Retained ladders are historical retirement/Undo references, never dispatch authority.
func transferRetiredLadderReferences(ctx context.Context, tx pgx.Tx, before, after []Profile) error {
	for _, old := range before {
		candidates := sameRouteProfiles(after, old)
		if len(candidates) == 0 {
			continue
		}
		replacement := nearestProfile(candidates, old.Effort, old.EffortLevel)
		for _, table := range []string{"model_role_routes", "model_security_role_routes"} {
			var incompatible bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` a JOIN `+table+` b ON b.tenant_id=a.tenant_id AND b.role=a.role WHERE a.profile_id=$1 AND b.profile_id=$2 AND (a.state<>b.state OR a.reason<>b.reason OR a.valid_until IS DISTINCT FROM b.valid_until))`, old.ID, replacement.ID).Scan(&incompatible); err != nil {
				return err
			}
			if incompatible {
				return fail(409, "successor has a conflicting role hold")
			}
			// Equal policies collapse into their earliest priority. Differing
			// pause/hold evidence rejects the transaction instead of dropping it.
			if _, err := tx.Exec(ctx, `DELETE FROM `+table+` t USING `+table+` a,`+table+` b WHERE a.tenant_id=b.tenant_id AND a.role=b.role AND a.profile_id=$1 AND b.profile_id=$2 AND t.tenant_id=a.tenant_id AND t.role=a.role AND t.priority=greatest(a.priority,b.priority)`, old.ID, replacement.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE `+table+` SET profile_id=$2 WHERE profile_id=$1 AND NOT EXISTS(SELECT 1 FROM `+table+` n WHERE n.tenant_id=`+table+`.tenant_id AND n.role=`+table+`.role AND n.profile_id=$2)`, old.ID, replacement.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
