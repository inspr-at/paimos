// SPDX-License-Identifier: AGPL-3.0-only

package agentplan

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestDecodePlanAndLegacyMapping(t *testing.T) {
	for _, tc := range []struct {
		name, raw, source string
		total             int
		limits            map[string]Limit
	}{
		{"unset", "", "default", 15, map[string]Limit{}},
		{"null preference", "null", "default", 15, map[string]Limit{}},
		{"legacy unset", `{"view":"area","area":{"backend":3}}`, "legacy", 15, map[string]Limit{}},
		{"legacy reservations ignored", `{"cap":12,"view":"model","area":{"backend":2},"model":{"codex":4,"cursor":0}}`, "legacy", 12, map[string]Limit{}},
		{"zero total", `{"total":0}`, "plan", 0, map[string]Limit{}},
		{"all states", `{"total":30,"limits":{"claude":"no_limit","codex":0,"cursor":"off","pi":30}}`, "plan", 30, map[string]Limit{"claude": {Mode: NoLimit}, "codex": {Mode: AtMost, Value: 0}, "cursor": {Mode: Off}, "pi": {Mode: AtMost, Value: 30}}},
		{"limits can sum above total", `{"total":1,"limits":{"claude":30,"codex":30}}`, "plan", 1, map[string]Limit{"claude": {Mode: AtMost, Value: 30}, "codex": {Mode: AtMost, Value: 30}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, source, err := Decode([]byte(tc.raw))
			if err != nil || source != tc.source || p.Total != tc.total || !reflect.DeepEqual(p.Limits, tc.limits) {
				t.Fatalf("Decode = %+v, %s, %v", p, source, err)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			mapped, source, err := Decode(raw)
			if err != nil || source != "plan" || !reflect.DeepEqual(mapped, p) {
				t.Fatalf("round trip = %+v, %s, %v", mapped, source, err)
			}
		})
	}
}

func TestRejectInvalidPlans(t *testing.T) {
	for _, raw := range []string{
		`[]`, `1`, `"plan"`, `{"total":-1}`, `{"total":31}`, `{"total":1.5}`, `{"total":"5"}`, `{"total":null}`,
		`{"limits":{}}`, `{"total":2,"cap":2}`, `{"total":2,"extra":true}`, `{"total":2,"limits":null}`,
		`{"total":2,"limits":[]}`, `{"total":2,"limits":{"codex":-1}}`, `{"total":2,"limits":{"codex":31}}`,
		`{"total":2,"limits":{"codex":1.5}}`, `{"total":2,"limits":{"codex":null}}`, `{"total":2,"limits":{"codex":true}}`,
		`{"total":2,"limits":{"codex":"0"}}`, `{"total":2,"limits":{"codex":"unlimited"}}`, `{"total":2,"limits":{"codex":"at_most"}}`, `{"total":2,"limits":{"codex":{}}}`,
		`{"total":2,"limits":{"unknown":2}}`, `{"cap":0}`, `{"cap":13}`, `{"cap":null}`, `{"cap":1.5}`,
		`{"view":"unknown"}`, `{"other":1}`, `{"total":2} {"total":1}`,
	} {
		if _, _, err := Decode([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, p := range []Plan{{Total: -1}, {Total: 31}, {Total: 5, Limits: map[string]Limit{"codex": {}}}, {Total: 5, Limits: map[string]Limit{"codex": {Mode: Off, Value: 2}}}} {
		if err := p.Validate(); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
}

func TestCanStart(t *testing.T) {
	for _, tc := range []struct {
		name, raw, harness, reason string
		running                    map[string]int
	}{
		{"room", `{"total":5}`, "codex", "", map[string]int{"claude": 3}},
		{"total at boundary", `{"total":5}`, "codex", "planned total reached", map[string]int{"claude": 3, "codex": 2}},
		{"wind down", `{"total":2}`, "codex", "planned total reached", map[string]int{"claude": 3, "codex": 2}},
		{"zero plan", `{"total":0}`, "codex", "planned total reached", nil},
		{"off", `{"total":5,"limits":{"cursor":"off"}}`, "cursor", "Cursor is off", nil},
		{"zero limit", `{"total":5,"limits":{"codex":0}}`, "codex", "Codex at its limit", nil},
		{"limit boundary", `{"total":5,"limits":{"codex":2}}`, "codex", "Codex at its limit", map[string]int{"codex": 2}},
		{"under limit", `{"total":5,"limits":{"codex":2}}`, "codex", "", map[string]int{"codex": 1}},
		{"explicit no limit", `{"total":30,"limits":{"codex":"no_limit"}}`, "codex", "", map[string]int{"codex": 20}},
		{"off running counts", `{"total":3,"limits":{"cursor":"off"}}`, "codex", "planned total reached", map[string]int{"cursor": 3}},
		{"unknown running counts", `{"total":3}`, "codex", "planned total reached", map[string]int{"future-harness": 3}},
		{"overflow", `{"total":30}`, "codex", "planned total reached", map[string]int{"codex": math.MaxInt, "claude": math.MaxInt}},
		{"negative running", `{"total":30}`, "codex", "invalid running count", map[string]int{"codex": -1}},
		{"unknown harness", `{"total":3}`, "other", "unknown harness", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, _, err := Decode([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(plan)
			ok, reason := CanStart(plan, tc.running, tc.harness)
			if ok != (tc.reason == "") || reason != tc.reason {
				t.Fatalf("CanStart = %v, %q; want %q", ok, reason, tc.reason)
			}
			after, _ := json.Marshal(plan)
			if string(before) != string(after) {
				t.Fatal("start decision mutated plan")
			}
		})
	}
	if ok, reason := CanStart(Plan{Total: 31}, nil, "codex"); ok || reason != "invalid plan" {
		t.Fatal("invalid plan allowed a start")
	}
}
