// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"github.com/inspr-at/paimos/internal/modelprefs"
	"slices"
	"strings"
)

// The displayed pick retains the stored first entry even when it cannot run.
func rawBoardDecision(s modelprefs.BoardState, column, situation string) modelprefs.BoardDecision {
	s.Lines = slices.Clone(s.Lines)
	for i := range s.Lines {
		s.Lines[i].Tools, s.Lines[i].Review, s.Lines[i].Residency = true, true, nil
	}
	return modelprefs.ResolveBoard(s, modelprefs.BoardQuery{Column: column, Situation: situation}, nil)
}

func setNativeEffort(s modelprefs.BoardState, c boardCatalog, level string, o *modelprefs.BoardOrder) error {
	if o.Effort == nil {
		return nil
	}
	if len(*o.Effort) > 32 || !effortRE.MatchString(*o.Effort) {
		return prefFail(422, "invalid_effort")
	}
	if strings.HasPrefix(o.Column, "review:") && *o.Effort != "xhigh" {
		return prefFail(422, "review_requires_xhigh")
	}
	if level == "default" {
		s.Person = nil
	}
	s.Lines = c.lines
	d := rawBoardDecision(s, o.Column, o.Situation)
	if len(d.Rank) == 0 {
		return prefFail(422, "no_first_line")
	}
	for _, p := range c.effortCandidates(d.Rank[0], *o.Effort, d.EffortLevel, false) {
		if p.Effort == *o.Effort {
			o.EffortLevel = p.EffortLevel
			return nil
		}
	}
	return prefFail(422, "effort_not_registered")
}

func carryNativeEffort(c boardCatalog, o *modelprefs.BoardOrder) error {
	if o.Effort == nil || len(o.Rank) == 0 {
		return nil
	}
	level := 3
	if o.EffortLevel != nil {
		level = *o.EffortLevel
	}
	ps := c.effortCandidates(o.Rank[0], *o.Effort, level, strings.HasPrefix(o.Column, "review:"))
	if len(ps) == 0 {
		return prefFail(422, "no_registered_effort")
	}
	effort := ps[0].Effort
	o.Effort, o.EffortLevel = &effort, ps[0].EffortLevel
	return nil
}
