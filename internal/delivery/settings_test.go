// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"strings"
	"testing"
)

func TestBuildSettingsClosedShapeAndTightening(t *testing.T) {
	parse := func(raw string) BuildSettings {
		t.Helper()
		s, e := ParseBuildSettings([]byte(raw))
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	base := parse(`{"budget_agent_hours":8,"max_agents":4,"largest_ticket_hours":3,"window":{"timezone":"UTC","slots":[{"days":[6],"from":"22:00","to":"02:00"}]}}`)
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{}`, true}, {`{"budget_agent_hours":4,"max_agents":2}`, true}, {`{"budget_agent_hours":9}`, false}, {`{"max_agents":5}`, false}, {`{"largest_ticket_hours":4}`, false},
		{`{"window":{"timezone":"UTC","slots":[{"days":[0],"from":"00:00","to":"01:00"}]}}`, true},
		{`{"window":{"timezone":"UTC","slots":[{"days":[0],"from":"01:00","to":"03:00"}]}}`, false},
	} {
		if got := Tightens(base, parse(tc.raw)); got != tc.want {
			t.Errorf("%s tighten=%v", tc.raw, got)
		}
	}
	if Tightens(BuildSettings{}, parse(`{"budget_agent_hours":1}`)) || Tightens(BuildSettings{}, parse(`{"window":{"timezone":"UTC","slots":[]}}`)) {
		t.Fatal("missing budget/window default did not need deploy")
	}
	resolved, sources := ResolveBuildSettings(base, parse(`{"max_agents":2,"budget_agent_hours":null}`))
	if *resolved.MaxAgents != 2 || *resolved.BudgetAgentHours != 8 || sources["max_agents"] != "overridden" || sources["budget_agent_hours"] != "inherited" {
		t.Fatalf("resolution=%+v %v", resolved, sources)
	}
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"unknown":1}`, `{"max_agents":0}`, `{"budget_agent_hours":-1}`, `{"window":{"timezone":"bad","slots":[]}}`, `{"window":{"timezone":"UTC","slots":[{"days":[0,0],"from":"12:00","to":"13:00"}]}}`, `{"window":{"timezone":"UTC","slots":[{"days":[0],"from":"24:00","to":"13:00"}]}}`, strings.Repeat(" ", 2049)} {
		if _, e := ParseBuildSettings([]byte(raw)); e == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
