// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/workorders"
)

// Hosts describe launcher admission scope; Agents, when present, are the exact
// generations to wind down. They are deliberately independent: an idle host
// can be selected without any agents, and a subset of a host's agents can be
// selected without reserving that host. This package stores the host policy;
// enforcing it at launch is the downstream launcher integration.
type leavingScope struct {
	Hosts  json.RawMessage `json:"hosts"`
	Agents *[]string       `json:"agents,omitempty"`
}

func (s *leavingScope) normalize() error {
	if len(s.Hosts) == 0 {
		s.Hosts = json.RawMessage(`"all"`)
	}
	var all string
	if err := json.Unmarshal(s.Hosts, &all); err == nil && all == "all" {
		s.Hosts = json.RawMessage(`"all"`)
	} else {
		var hosts []string
		if err := json.Unmarshal(s.Hosts, &hosts); err != nil || hosts == nil || len(hosts) > 200 {
			return workorders.Fail(400, "hosts must be all or an array of at most 200 host identities")
		}
		for _, host := range hosts {
			if !validHostText(host) {
				return workorders.Fail(400, "invalid wind-down host")
			}
		}
		slices.Sort(hosts)
		s.Hosts, _ = json.Marshal(slices.Compact(hosts))
	}
	if s.Agents != nil {
		if len(*s.Agents) > 200 {
			return workorders.Fail(400, "at most 200 wind-down agents permitted")
		}
		for i, id := range *s.Agents {
			if !workorders.UUID(id) {
				return workorders.Fail(400, "invalid wind-down agent")
			}
			(*s.Agents)[i] = strings.ToLower(id)
		}
		slices.Sort(*s.Agents)
		*s.Agents = slices.Compact(*s.Agents)
	}
	return nil
}

func (s leavingScope) selects(session Session) bool {
	if s.Agents != nil {
		return slices.Contains(*s.Agents, session.ID)
	}
	if string(s.Hosts) == `"all"` {
		return true
	}
	var hosts []string
	_ = json.Unmarshal(s.Hosts, &hosts) // normalized at the API boundary
	return slices.Contains(hosts, session.Host)
}

func (s leavingScope) equal(other leavingScope) bool {
	return string(s.Hosts) == string(other.Hosts) && (s.Agents == nil && other.Agents == nil || s.Agents != nil && other.Agents != nil && slices.Equal(*s.Agents, *other.Agents))
}
