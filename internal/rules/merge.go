// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
)

// Merge evaluates expiration before precedence, including disabled exceptions.
// No cached rendered text is used as input. Equal-rank identities are ambiguous.
// A lower layer never replaces a higher identity, including locked rules; any
// tightening must use a distinct identity instead of guessing prose semantics.
// It applies the default budget; MergeWithin applies a workspace's own.
func Merge(c Context, snapshots []Snapshot, now time.Time) (Merged, error) {
	return merge(c, snapshots, now, false, nil, DefaultBudget(), doctrine.Catalog{})
}

// MergeWithin is Merge under a workspace's configured budget.
func MergeWithin(c Context, snapshots []Snapshot, now time.Time, b Budget) (Merged, error) {
	return merge(c, snapshots, now, false, nil, b, doctrine.Catalog{})
}

// MergeDelivered is Merge for a workspace that pins doctrine. Git-backed rules
// (AEON-318 identities, or the same text) stay on the harness channel: they are
// omitted here, and the session file names the expected release instead.
// Doctrine bytes are not added to the session budget.
func MergeDelivered(c Context, snapshots []Snapshot, now time.Time, cat doctrine.Catalog) (Merged, error) {
	return merge(c, snapshots, now, false, nil, DefaultBudget(), cat)
}

// MergeDeliveredWithin is MergeDelivered under a workspace's configured budget.
func MergeDeliveredWithin(c Context, snapshots []Snapshot, now time.Time, b Budget, cat doctrine.Catalog) (Merged, error) {
	return merge(c, snapshots, now, false, nil, b, cat)
}

// errStopped is returned when stop reports that the caller's time is up.
var errStopped = &Error{Status: 503, Code: "busy", Message: "the rules store is busy; nothing was changed, try again"}

// merge is Merge with two options for callers that render many contexts over
// the same snapshots: validated means the caller already checked every
// snapshot once (validSnapshot) and ordered them by rank and set id
// (sortSnapshots), and stop, checked per snapshot, ends the work early when the
// caller's deadline has passed.
func merge(c Context, snapshots []Snapshot, now time.Time, validated bool, stop func() bool, b Budget, cat doctrine.Catalog) (Merged, error) {
	r, err := render(c, snapshots, now, validated, stop, cat)
	if err != nil {
		return r.Merged, err
	}
	if err = b.enforce(r.Merged, r.usage); err != nil {
		return r.Merged, err
	}
	if r.Floor == "" {
		return r.Merged, fail(409, "floor_missing", "publish an applicable locked company safety floor before using session rules")
	}
	return r.Merged, nil
}

// rendered is one merged file plus what people need to read it: the set and
// layer each served rule came from, and how many bytes each layer takes.
type rendered struct {
	Merged
	from  map[string]Snapshot
	usage LayerBytes
}

// render builds the merged file without judging its size or floor. Rules the
// doctrine catalog delivers on the harness channel are omitted (AEON-320).
func render(c Context, snapshots []Snapshot, now time.Time, validated bool, stop func() bool, cat doctrine.Catalog) (rendered, error) {
	out := rendered{Merged: Merged{Context: c, Versions: []VersionRef{}, Rules: []Rule{}, Version: "floor-only"}, from: map[string]Snapshot{}}
	if err := ValidateContext(c); err != nil {
		return out, err
	}
	ordered := snapshots
	if !validated {
		ordered = slices.Clone(snapshots)
		slices.SortFunc(ordered, func(a, b Snapshot) int {
			if d := a.Scope.rank() - b.Scope.rank(); d != 0 {
				return d
			}
			return strings.Compare(a.SetID, b.SetID)
		})
	}
	chosen := map[string]Rule{}
	ranks := map[string]int{}
	companyFloor := map[string]Rule{}
	for _, s := range ordered {
		if stop != nil && stop() {
			return out, errStopped
		}
		if !validated {
			if err := ValidateScope(s.Scope); err != nil {
				return out, err
			}
		}
		if !s.Scope.matches(c) {
			continue
		}
		if !validated {
			if err := validSnapshot(s); err != nil {
				return out, err
			}
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
			// Details load on demand and explanations are for people: agents
			// receive neither, so the served bytes never depend on them.
			r.Details = ""
			r.TLDR = nil
			chosen[r.Identity] = r
			ranks[r.Identity] = s.Scope.rank()
			out.from[r.Identity] = s
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
	body.WriteString(SessionHeader)
	// The pointer is session-channel bytes. Doctrine text is not.
	body.WriteString(cat.Pointer())
	for _, k := range keys {
		r := chosen[k]
		if !r.Enabled {
			continue
		}
		// Resolve precedence, selectors and expiry before choosing a delivery
		// channel. An omitted doctrine copy must still suppress lower layers.
		if hit, copied := cat.Match(r.Text, r.Source.Identity, r.Source.Reference); copied {
			if _, locked := companyFloor[k]; locked {
				if pin := cat.FloorPointer(r.Identity, hit); pin != "" {
					body.WriteString(pin)
					floor.WriteString(pin)
					// The pointer is session-channel bytes on the floor's layer.
					out.usage.add(out.from[k].Scope.Layer, len(pin))
				}
			}
			continue
		}
		out.Rules = append(out.Rules, r)
		l := ruleLine(r)
		body.WriteString(l)
		out.usage.add(out.from[k].Scope.Layer, len(l))
		if _, ok := companyFloor[k]; ok {
			floor.WriteString(l)
		}
	}
	out.Body = body.String()
	out.Floor = floor.String()
	out.ByteSize = len(out.Body)
	out.SHA256 = digest([]byte(out.Body))
	return out, nil
}

// Budget is a workspace's always-on budget (AEON-314): the whole merged file
// and, optionally, each layer's own share of it. The caps are ceilings; their
// sum may stay below the total. A zero cap means the layer has no own cap.
type Budget struct {
	MaxBytes int        `json:"max_bytes"`
	Layers   LayerBytes `json:"layer_max_bytes"`
}

// LayerBytes holds one number per layer: caps in a Budget, usage in a report.
type LayerBytes struct {
	Company int `json:"company,omitempty"`
	Project int `json:"project,omitempty"`
	Person  int `json:"person,omitempty"`
	Agent   int `json:"agent,omitempty"`
}

// LayerNames is the order people read layers in, highest precedence first.
var LayerNames = []string{"company", "project", "person", "agent"}

func (l *LayerBytes) ref(layer string) *int {
	switch layer {
	case "company":
		return &l.Company
	case "project":
		return &l.Project
	case "person":
		return &l.Person
	default:
		return &l.Agent
	}
}
func (l *LayerBytes) add(layer string, n int) { *l.ref(layer) += n }

// Of returns the number for one layer.
func (l LayerBytes) Of(layer string) int { return *l.ref(layer) }

func DefaultBudget() Budget { return Budget{MaxBytes: MaxBytes} }

// Validate keeps a budget inside the product bounds: the total between
// MinBudgetBytes and MaxBytes, and every layer cap between 500 bytes and
// the total.
func (b Budget) Validate() error {
	if b.MaxBytes < MinBudgetBytes || b.MaxBytes > MaxBytes {
		return fail(400, "invalid_budget", fmt.Sprintf("the session file budget must be between %d and %d bytes", MinBudgetBytes, MaxBytes))
	}
	for _, layer := range LayerNames {
		if v := b.Layers.Of(layer); v != 0 && (v < MinLayerBytes || v > b.MaxBytes) {
			return fail(400, "invalid_budget", fmt.Sprintf("a layer cap must be between %d bytes and the total budget", MinLayerBytes))
		}
	}
	return nil
}

// MinLayerBytes is the smallest own cap a layer can have.
const MinLayerBytes = 500

// enforce refuses a file over the total or over one layer's own cap.
func (b Budget) enforce(m Merged, usage LayerBytes) error {
	if m.ByteSize > b.MaxBytes {
		return &Error{Status: 422, Code: "rules_budget_exceeded", Message: fmt.Sprintf("always-on rules require %d UTF-8 bytes (limit %d); shorten text or move explanation to details", m.ByteSize, b.MaxBytes), ActualBytes: m.ByteSize, MaxBytes: b.MaxBytes}
	}
	for _, layer := range LayerNames {
		if limit, used := b.Layers.Of(layer), usage.Of(layer); limit > 0 && used > limit {
			return &Error{Status: 422, Code: "rules_budget_exceeded", Message: fmt.Sprintf("%s rules require %d UTF-8 bytes of the session file (their cap is %d); shorten text or move explanation to details", layer, used, limit), ActualBytes: used, MaxBytes: limit, Layer: layer}
		}
	}
	return nil
}

// validSnapshot is the integrity check Merge runs on every snapshot it uses.
func validSnapshot(s Snapshot) error {
	if err := ValidateScope(s.Scope); err != nil {
		return err
	}
	if !releasehistory.ValidVersion(s.Version) || s.SHA256 != SnapshotDigest(s) {
		return fail(409, "snapshot_integrity", "invalid snapshot version or digest")
	}
	return ValidateRules(s.Rules)
}

// SnapshotDigest excludes the digest itself and publication timestamp. An empty
// note is omitted, so a snapshot stored before notes existed keeps its digest.
// A non-empty note is part of the immutable bytes. A retry with the same
// version, revision and note resolves to the original stored snapshot.
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
