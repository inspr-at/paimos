// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/releasehistory"
)

// Merge evaluates expiration before precedence, including disabled exceptions.
// No cached rendered text is used as input. Equal-rank identities are ambiguous.
// A lower layer never replaces a higher identity, including locked rules; any
// tightening must use a distinct identity instead of guessing prose semantics.
func Merge(c Context, snapshots []Snapshot, now time.Time) (Merged, error) {
	out := Merged{Context: c, Versions: []VersionRef{}, Rules: []Rule{}, Version: "floor-only"}
	if err := ValidateContext(c); err != nil {
		return out, err
	}
	ordered := slices.Clone(snapshots)
	slices.SortFunc(ordered, func(a, b Snapshot) int {
		if d := a.Scope.rank() - b.Scope.rank(); d != 0 {
			return d
		}
		return strings.Compare(a.SetID, b.SetID)
	})
	chosen := map[string]Rule{}
	ranks := map[string]int{}
	companyFloor := map[string]Rule{}
	for _, s := range ordered {
		if err := ValidateScope(s.Scope); err != nil {
			return out, err
		}
		if !s.Scope.matches(c) {
			continue
		}
		if !releasehistory.ValidVersion(s.Version) || s.SHA256 != SnapshotDigest(s) {
			return out, fail(409, "snapshot_integrity", "invalid snapshot version or digest")
		}
		if err := ValidateRules(s.Rules); err != nil {
			return out, err
		}
		out.Versions = append(out.Versions, VersionRef{s.SetID, s.Version, s.SHA256})
		if out.Version == "floor-only" || s.Version > out.Version {
			out.Version = s.Version
		}
		for _, r := range s.Rules {
			if len(r.Roles) > 0 && !slices.Contains(r.Roles, c.Role) || len(r.Harnesses) > 0 && !slices.Contains(r.Harnesses, c.Harness) {
				continue
			}
			if r.ExpiresAt != nil {
				if !r.ExpiresAt.After(now) {
					continue
				}
				if out.ValidUntil == nil || r.ExpiresAt.Before(*out.ValidUntil) {
					at := *r.ExpiresAt
					out.ValidUntil = &at
				}
			}
			if rank, exists := ranks[r.Identity]; exists {
				if rank == s.Scope.rank() {
					return out, fail(409, "ambiguous_identity", "multiple published sets at the same precedence contain identity "+r.Identity)
				}
				continue
			}
			r.Details = ""
			chosen[r.Identity] = r
			ranks[r.Identity] = s.Scope.rank()
			if s.Scope.Layer == "company" && r.Strength == "locked" {
				companyFloor[r.Identity] = r
			}
		}
	}
	keys := make([]string, 0, len(chosen))
	for k := range chosen {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var body, floor strings.Builder
	body.WriteString("# Aeon session rules\n\n")
	for _, k := range keys {
		r := chosen[k]
		if !r.Enabled {
			continue
		}
		out.Rules = append(out.Rules, r)
		body.WriteString(ruleLine(r))
		if _, ok := companyFloor[k]; ok {
			floor.WriteString(ruleLine(r))
		}
	}
	out.Body = body.String()
	out.Floor = floor.String()
	out.ByteSize = len(out.Body)
	out.SHA256 = digest([]byte(out.Body))
	if out.ByteSize > MaxBytes {
		return out, &Error{Status: 422, Code: "rules_budget_exceeded", Message: fmt.Sprintf("always-on rules require %d UTF-8 bytes (limit %d); shorten text or move explanation to details", out.ByteSize, MaxBytes), ActualBytes: out.ByteSize, MaxBytes: MaxBytes}
	}
	if out.Floor == "" {
		return out, fail(409, "floor_missing", "publish an applicable locked company safety floor before using session rules")
	}
	return out, nil
}

// SnapshotDigest excludes the digest itself and publication timestamp; a retry
// with the same version and revision resolves to the original stored snapshot.
func SnapshotDigest(s Snapshot) string {
	s.SHA256 = ""
	s.PublishedAt = time.Time{}
	return jsonDigest(s)
}

// Stub is a reusable opt-in preview for AR6. The caller chooses installation;
// generating it never changes a harness file or claims instructions executed.
func Stub(m Merged) (string, error) {
	if m.Floor == "" || m.SHA256 != digest([]byte(m.Body)) || !containsFloor(m.Body, m.Floor) || len(m.Body) > MaxBytes {
		return "", fail(400, "invalid_floor", "verified company floor is required")
	}
	text := "Register this session with Aeon and explicitly load its returned rules. If unavailable, use the verified context-bound cache marked stale. Keep this locked company floor in force:\n\n" + m.Floor
	if len(text) > MaxBytes {
		return "", fail(422, "rules_budget_exceeded", "stub plus company floor exceeds 12000 UTF-8 bytes")
	}
	return text, nil
}
