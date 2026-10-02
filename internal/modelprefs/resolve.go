// SPDX-License-Identifier: AGPL-3.0-only

// Package modelprefs evaluates sparse Default → You → Project preferences.
// It has no dependency on the registry or account routing packages.
package modelprefs

import (
	"encoding/json"
	"strings"
)

type Cell struct {
	Mode      string `json:"mode"`
	ProfileID string `json:"profile_id,omitempty"`
	Family    string `json:"family,omitempty"`
	Line      string `json:"line,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Harness   string `json:"harness,omitempty"`
}
type Row struct {
	Locked bool            `json:"locked"`
	Cells  map[string]Cell `json:"cells"`
}
type Scope struct {
	ID              string         `json:"id"`
	Level           string         `json:"level"`
	PersonID        *string        `json:"person_id,omitempty"`
	ProjectID       *string        `json:"project_id,omitempty"`
	Residency       *string        `json:"residency"`
	ResidencyLocked bool           `json:"residency_locked"`
	PrefsLocked     bool           `json:"prefs_locked"`
	Revision        int64          `json:"revision"`
	Rows            map[string]Row `json:"rows"`
}
type CellResult struct {
	Cell         *Cell  `json:"cell,omitempty"`
	SetBy        string `json:"set_by"`
	LockedBy     string `json:"locked_by,omitempty"`
	KindFallback bool   `json:"kind_fallback"`
	Revision     int64  `json:"prefs_revision"`
}
type ResidencyResult struct {
	Value    string `json:"value"`
	SetBy    string `json:"set_by"`
	LockedBy string `json:"locked_by,omitempty"`
	// A residency lock is a strong default. The chosen override still applies.
	LoosenedLock  bool            `json:"loosened_lock"`
	LockValue     string          `json:"lock_value,omitempty"`
	LoosenedLocks []ResidencyLock `json:"loosened_locks,omitempty"`
}

type ResidencyLock struct {
	Level string `json:"level"`
	Value string `json:"value"`
}

func lockTop(chain []Scope, kind string) (int, string) {
	for i, s := range chain {
		if s.PrefsLocked || s.Rows[kind].Locked {
			return i, s.Level
		}
	}
	return len(chain) - 1, ""
}

func ResolveCell(chain []Scope, kind, bucket string) CellResult {
	out := CellResult{SetBy: "default"}
	top, locked := lockTop(chain, kind)
	out.LockedBy = locked
	walk := func(k string, end int) bool {
		for i := end; i >= 0; i-- {
			if cell, ok := chain[i].Rows[k].Cells[bucket]; ok {
				out.Cell = &cell
				out.SetBy = chain[i].Level
				out.Revision = chain[i].Revision
				return true
			}
		}
		return false
	}
	if walk(kind, top) {
		return out
	}
	if kind != "other" {
		otherTop, otherLock := lockTop(chain, "other")
		if otherTop < top {
			top = otherTop
			out.LockedBy = otherLock
		}
		if walk("other", top) {
			out.KindFallback = true
		}
	}
	return out
}

// ResolveResidency implements Markus's Q1 decision: every narrower value
// applies, including a looser value below a lock. Such an override is flagged.
func ResolveResidency(chain []Scope) ResidencyResult {
	out := ResidencyResult{Value: "any", SetBy: "default"}
	lockAt := -1
	selectedAt := -1
	for i, s := range chain {
		if s.Residency != nil {
			out.Value = NormalizeResidency(*s.Residency)
			out.SetBy = s.Level
			selectedAt = i
		}
		if s.ResidencyLocked && lockAt < 0 {
			lockAt = i
			out.LockedBy = s.Level
			out.LockValue = out.Value
		}
	}
	for i, s := range chain {
		if i < selectedAt && s.ResidencyLocked && s.Residency != nil && Strictness(out.Value) < Strictness(*s.Residency) {
			out.LoosenedLock = true
			out.LoosenedLocks = append(out.LoosenedLocks, ResidencyLock{Level: s.Level, Value: NormalizeResidency(*s.Residency)})
		}
	}

	return out
}

func NormalizeResidency(value string) string {
	switch strings.TrimSpace(value) {
	case "eu", "eu-e1":
		return "eu"
	case "local", "local-l1":
		return "local"
	default:
		return "any"
	}
}
func Strictness(value string) int {
	switch NormalizeResidency(value) {
	case "local":
		return 2
	case "eu":
		return 1
	default:
		return 0
	}
}
func Strictest(a, b string) string {
	if Strictness(b) > Strictness(a) {
		return NormalizeResidency(b)
	}
	return NormalizeResidency(a)
}

// Stamp stores any as NULL for compatibility with historical runs.
func Stamp(value string) *string {
	value = NormalizeResidency(value)
	if value == "any" {
		return nil
	}
	return &value
}
func BucketOf(complexity, ticketRole string) string {
	if complexity == "L" || complexity == "" && ticketRole == "build-hard" {
		return "complex"
	}
	return "normal"
}

type Placement struct {
	Area             string `json:"area"`
	Complexity       string `json:"complexity"`
	ComplexitySource string `json:"complexity_source"`
	RouteRole        string `json:"route_role"`
	Residency        string `json:"residency"`
}

// PlacementFields decodes JSON tokens before trimming. Invalid values are
// absent placement, never request failures or raw quoted area strings.
func PlacementFields(raw json.RawMessage) Placement {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	read := func(k string) string { var s string; _ = json.Unmarshal(fields[k], &s); return strings.TrimSpace(s) }
	return Placement{Area: read("area"), Complexity: read("complexity"), ComplexitySource: read("complexity_source"), RouteRole: read("route_role"), Residency: read("residency")}
}
