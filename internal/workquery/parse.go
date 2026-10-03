// SPDX-License-Identifier: AGPL-3.0-only
package workquery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var slugRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func Bounds(v url.Values) error {
	for _, key := range []string{"kind", "state", "work_state", "priority", "assignee", "tag", "cost_unit", "human_check", "epic", "ships_in", "hide_state"} {
		count, size := 0, 0
		for _, raw := range v[key] {
			count += strings.Count(raw, ",") + 1
			size += len(raw)
			if count > 100 || size > 6400 {
				return fmt.Errorf("%s lists at most 100 values / 6400 bytes", key)
			}
		}
	}
	if len(v.Get("q")) > 200 {
		return fmt.Errorf("q exceeds 200 UTF-8 bytes")
	}
	return nil
}
func Values(v url.Values, key string, check func(string) bool) (in, out []string, err error) {
	if err = Bounds(v); err != nil {
		return
	}
	for _, raw := range v[key] {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			negative := strings.HasPrefix(part, "!")
			part = strings.TrimPrefix(part, "!")
			if part == "" || (check != nil && !check(part)) {
				return nil, nil, fmt.Errorf("invalid %s", key)
			}
			if negative {
				out = append(out, part)
			} else {
				in = append(in, part)
			}
		}
	}
	slices.Sort(in)
	in = slices.Compact(in)
	slices.Sort(out)
	out = slices.Compact(out)
	return
}

// Parse reads ordinary work filters; lifecycle state is translated by the delivery adapter.
func Parse(v url.Values) (Query, error) {
	q := Query{Q: strings.TrimSpace(v.Get("q"))}
	if err := Bounds(v); err != nil {
		return q, err
	}
	specs := []struct {
		key     string
		in, out *[]string
		check   func(string) bool
	}{
		{"kind", &q.Kinds, &q.KindsNot, func(s string) bool { return uuidRE.MatchString(s) || slugRE.MatchString(s) }},
		{"state", &q.States, &q.StatesNot, nil}, {"priority", &q.Priorities, &q.PrioritiesNot, nil},
		{"assignee", &q.Assignees, &q.AssigneesNot, func(s string) bool { return s == "none" || uuidRE.MatchString(s) }},
		{"tag", &q.Tags, &q.TagsNot, func(s string) bool { return len(s) <= 200 }}, {"cost_unit", &q.CostUnits, &q.CostUnitsNot, func(s string) bool { return len(s) <= 200 }},
		{"human_check", &q.HumanChecks, &q.HumanChecksNot, func(s string) bool { return s == "pending" || s == "none" }},
		{"epic", &q.Epics, &q.EpicsNot, func(s string) bool { return s == "none" || uuidRE.MatchString(s) }},
	}
	for _, spec := range specs {
		in, out, err := Values(v, spec.key, spec.check)
		if err != nil {
			return q, err
		}
		if slices.Contains([]string{"kind", "assignee", "epic", "tag", "cost_unit"}, spec.key) {
			for i := range in {
				in[i] = strings.ToLower(in[i])
			}
			for i := range out {
				out[i] = strings.ToLower(out[i])
			}
		}
		slices.Sort(in)
		in = slices.Compact(in)
		slices.Sort(out)
		out = slices.Compact(out)
		*spec.in, *spec.out = in, out
	}
	q.DateField = v.Get("date_field")
	if q.DateField == "" && (v.Has("date_from") || v.Has("date_to")) {
		return q, fmt.Errorf("date bounds need date_field")
	}
	if q.DateField != "" && !slices.Contains([]string{"created", "updated", "start", "end", "accepted"}, q.DateField) {
		return q, fmt.Errorf("invalid date_field")
	}
	for key, dst := range map[string]**time.Time{"date_from": &q.DateFrom, "date_to": &q.DateTo} {
		if v.Has(key) {
			at, err := time.Parse(time.RFC3339, v.Get(key))
			if err != nil {
				return q, fmt.Errorf("invalid %s", key)
			}
			*dst = &at
		}
	}
	if q.DateFrom != nil && q.DateTo != nil && !q.DateTo.After(*q.DateFrom) {
		return q, fmt.Errorf("date_to must be after date_from")
	}
	return q, nil
}
func Fingerprint(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
