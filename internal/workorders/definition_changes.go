// SPDX-License-Identifier: AGPL-3.0-only
package workorders

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefinitionCopyState separates immutable provenance from per-check decisions.
// Neither snapshot participates in entry enforcement, which reads Checks only.
type DefinitionCopyState struct {
	Base     map[string][]EntryRule `json:"copied_from_checks"`
	Reviewed map[string][]EntryRule `json:"reviewed_checks"`
}

func DefinitionCopyStateTx(ctx context.Context, tx pgx.Tx, id string) (DefinitionCopyState, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT fields FROM nodes WHERE id=$1`, id).Scan(&raw); err != nil {
		return DefinitionCopyState{}, err
	}
	return DecodeDefinitionCopyState(raw)
}

func DecodeDefinitionCopyState(raw []byte) (DefinitionCopyState, error) {
	var state DefinitionCopyState
	if len(raw) > 65536 || json.Unmarshal(raw, &state) != nil {
		return state, Fail(409, "invalid definition copy state")
	}
	for _, checks := range []map[string][]EntryRule{state.Base, state.Reviewed} {
		if checks != nil {
			if err := ValidateEntryRules(checks); err != nil {
				return state, Fail(409, "invalid definition copy state")
			}
		}
	}
	if state.Reviewed == nil {
		state.Reviewed = state.Base
	}
	return state, nil
}

type DefinitionChange struct {
	CheckID string     `json:"check_id"`
	Block   string     `json:"block"`
	Kind    string     `json:"kind"`
	Before  *EntryRule `json:"before"`
	Current *EntryRule `json:"current"`
}

type definitionPosition struct {
	block string
	rule  EntryRule
}

func definitionPositions(checks map[string][]EntryRule) map[string]definitionPosition {
	out := make(map[string]definitionPosition)
	for _, block := range []string{"in_progress", "qa", "done"} {
		for _, rule := range checks[block] {
			out[rule.ID] = definitionPosition{block, rule}
		}
	}
	return out
}

func sameDefinitionRule(a, b EntryRule) bool {
	normalize := func(r EntryRule) EntryRule {
		if r.Condition == "" {
			r.Condition = entryCatalog[r.ID].condition
			if r.Condition == "" {
				r.Condition = "all"
			}
		}
		if r.Label == "" {
			r.Label = entryCatalog[r.ID].en
		}
		if r.LabelDE == "" {
			r.LabelDE = entryCatalog[r.ID].de
		}
		r.Kinds = slices.Clone(r.Kinds)
		slices.Sort(r.Kinds)
		r.Labels = slices.Clone(r.Labels)
		for i := range r.Labels {
			r.Labels[i] = strings.ToLower(r.Labels[i])
		}
		slices.Sort(r.Labels)
		r.Labels = slices.Compact(r.Labels)
		if len(r.Kinds) == 0 {
			r.Kinds = nil
		}
		if len(r.Labels) == 0 {
			r.Labels = nil
		}
		return r
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

// Each changed check waits once, even when the project independently edited or
// left it out. Keeping it acknowledges this version, never future versions.
func PendingDefinitionChanges(reviewed, workspace map[string][]EntryRule) []DefinitionChange {
	before, current := definitionPositions(reviewed), definitionPositions(workspace)
	out := []DefinitionChange{}
	for _, block := range []string{"in_progress", "qa", "done"} {
		for _, rule := range workspace[block] {
			old, exists := before[rule.ID]
			if exists && old.block == block && sameDefinitionRule(old.rule, rule) {
				continue
			}
			change := DefinitionChange{CheckID: rule.ID, Block: block, Kind: "new", Current: &rule}
			if exists {
				oldRule := old.rule
				change.Before, change.Kind = &oldRule, "changed"
			}
			out = append(out, change)
		}
		for _, rule := range reviewed[block] {
			if _, exists := current[rule.ID]; !exists {
				out = append(out, DefinitionChange{CheckID: rule.ID, Block: block, Kind: "removed", Before: &rule})
			}
		}
	}
	return out
}

// Copy all slices before applying a decision, so the event's before snapshot
// and the immutable copied checks cannot be changed through aliasing.
func ApplyDefinitionChange(checks map[string][]EntryRule, change DefinitionChange) map[string][]EntryRule {
	out := make(map[string][]EntryRule)
	for block, rules := range checks {
		for _, rule := range rules {
			if rule.ID != change.CheckID {
				out[block] = append(out[block], rule)
			}
		}
	}
	if change.Current != nil {
		index := len(out[change.Block])
		for i, rule := range checks[change.Block] {
			if rule.ID == change.CheckID {
				index = min(i, index)
				break
			}
		}
		out[change.Block] = slices.Insert(out[change.Block], index, *change.Current)
	}
	return out
}

// Count actual copy differences, including order changes, independently of
// waiting changes. An identical own copy is not a differing project.
func DefinitionDifferenceCount(own, workspace map[string][]EntryRule) int {
	left, right := definitionPositions(own), definitionPositions(workspace)
	count := 0
	for id, pos := range left {
		other, ok := right[id]
		if !ok || pos.block != other.block || !sameDefinitionRule(pos.rule, other.rule) {
			count++
		}
	}
	for id := range right {
		if _, ok := left[id]; !ok {
			count++
		}
	}
	for _, block := range []string{"in_progress", "qa", "done"} {
		a, b := []string{}, []string{}
		for _, rule := range own[block] {
			if p, ok := right[rule.ID]; ok && p.block == block {
				a = append(a, rule.ID)
			}
		}
		for _, rule := range workspace[block] {
			if p, ok := left[rule.ID]; ok && p.block == block {
				b = append(b, rule.ID)
			}
		}
		if !slices.Equal(a, b) {
			count++
		}
	}
	return count
}

type DefinitionProject struct {
	ProjectID  string
	Key        string
	Definition *EntryDefinition
	Copy       DefinitionCopyState
}

// Callers supply at most 1000 currently readable project IDs. The lateral
// limit detects duplicate definitions without decoding an unbounded list.
func DefinitionProjectsTx(ctx context.Context, tx pgx.Tx, ids []string) ([]DefinitionProject, error) {
	if len(ids) > 1000 {
		return nil, Fail(422, "definition project coverage exceeds 1000 projects")
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text,coalesce(nullif(btrim(p.fields->>'project_key'),''),nullif(btrim(p.fields#>>'{classic,key}'),''),p.key),d.id::text,d.updated_at,d.created_at,d.fields
	 FROM nodes p JOIN node_kinds k ON k.tenant_id=p.tenant_id AND k.id=p.kind_id
	 LEFT JOIN LATERAL (SELECT n.id,n.updated_at,n.created_at,n.fields FROM nodes n
	 JOIN node_kinds dk ON dk.tenant_id=n.tenant_id AND dk.id=n.kind_id
	 WHERE n.tenant_id=p.tenant_id AND n.parent_id=p.id AND n.deleted_at IS NULL AND dk.slug='definition'
	 ORDER BY n.id LIMIT 2) d ON true
	 WHERE p.id=ANY($1::uuid[]) AND p.deleted_at IS NULL AND k.slug='project' ORDER BY p.id,d.id LIMIT 2001`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DefinitionProject{}
	for rows.Next() {
		var project DefinitionProject
		var id *string
		var revision, created *time.Time
		var raw []byte
		if err := rows.Scan(&project.ProjectID, &project.Key, &id, &revision, &created, &raw); err != nil {
			return nil, err
		}
		if len(out) > 0 && out[len(out)-1].ProjectID == project.ProjectID {
			return nil, Fail(409, "multiple definitions at this scope")
		}
		if id != nil {
			project.Copy, err = DecodeDefinitionCopyState(raw)
			if err != nil {
				return nil, err
			}
			var fields struct {
				Checks     map[string][]EntryRule `json:"checks"`
				CopiedFrom *time.Time             `json:"copied_from_revision"`
			}
			if json.Unmarshal(raw, &fields) != nil || ValidateEntryRules(fields.Checks) != nil {
				return nil, Fail(409, "invalid definition checks")
			}
			project.Definition = &EntryDefinition{ID: *id, Revision: *revision, CreatedAt: created, Source: "project", CopiedFrom: fields.CopiedFrom, Checks: fields.Checks}
		}
		out = append(out, project)
	}
	return out, rows.Err()
}
