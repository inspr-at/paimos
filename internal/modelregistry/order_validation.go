// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/modelprefs"
)

func copyOrderEfforts(efforts map[string]string, rank []string) map[string]string {
	out := map[string]string{}
	for _, line := range rank {
		if effort, ok := efforts[line]; ok {
			out[line] = effort
		}
	}
	return out
}

func validateModelsOrder(c boardCatalog, o modelprefs.BoardOrder) error {
	if err := validateBoardLines(c, o.Rank, o.Not); err != nil {
		return err
	}
	if o.End != "" && o.End != "wait" && o.End != "default" && o.End != "row" && o.End != "auto" ||
		o.Situation == "first" && (o.End == "row" || o.End == "auto" || o.Column == "other" && o.End == "default") ||
		o.Situation != "first" && o.End == "default" || o.Situation == "review" && o.End == "row" || o.Situation != "review" && o.End == "auto" {
		return prefFail(422, "invalid_end")
	}
	for line, effort := range o.Efforts {
		if !slices.Contains(o.Rank, line) || len(effort) > 32 || !effortRE.MatchString(effort) {
			return prefFail(422, "invalid_effort")
		}
		if !slices.ContainsFunc(c.profiles[line], func(p Profile) bool { return p.Enabled && !p.Retired && p.Effort == effort }) {
			return prefFail(422, "effort_not_registered")
		}
	}
	if o.Situation == "review" || strings.HasPrefix(o.Column, "review:") {
		for _, line := range o.Rank {
			qualified := false
			for _, profile := range modelsRouteCandidates(c.profiles[line], "", modelsReviewMinLevel, true) {
				if profile.Enabled && !profile.Retired && qualifiedReviewProfile(profile) && (o.Efforts[line] == "" || o.Efforts[line] == profile.Effort) {
					qualified = true
				}
			}
			if !qualified {
				return prefFail(422, "review_floor")
			}
		}
	}
	return nil
}
