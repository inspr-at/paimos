// SPDX-License-Identifier: AGPL-3.0-only
package modelprefs

import (
	"regexp"
	"strconv"
	"strings"
)

// ReviewHighestNative is the reversible 2026-10-10 review-floor decision:
// reviews take each route's highest native effort level.
const ReviewHighestNative = true

var codexLineRE = regexp.MustCompile(`^gpt-([0-9]+(?:\.[0-9]+)*)-(.+)$`)
var concreteLineRE = regexp.MustCompile(`^([a-z][a-z-]*?)-?([0-9]+(?:[.-][0-9]+)*)(.*)$`)
var cursorEffortRE = regexp.MustCompile(`^(.+?)(-(low|medium|high|xhigh|max|ultra))?(-fast)?$`)

// ModelLine derives the vendor line and numeric version from an immutable
// harness model id. The registry's catalog revision is deliberately unrelated.
func ModelLine(harness, model string) (line, version string) {
	id := model
	if harness == "codex" {
		if m := codexLineRE.FindStringSubmatch(id); m != nil {
			return m[2], m[1]
		}
		return id, ""
	}
	if harness == "opencode" {
		id = strings.TrimPrefix(id, "google/")
		id = strings.TrimPrefix(id, "ollama/")
	}
	if harness == "pi" {
		id = strings.TrimPrefix(id, "anthropic/")
	}
	if harness == "claude" || harness == "pi" {
		id = strings.TrimPrefix(id, "claude-")
		switch id {
		case "opus", "sonnet", "haiku", "fable":
			return id, "alias"
		}
	}
	suffix := ""
	if harness == "cursor" {
		if m := cursorEffortRE.FindStringSubmatch(id); m != nil {
			id = m[1]
			suffix = m[4]
		}
	}
	if m := concreteLineRE.FindStringSubmatch(id); m != nil {
		return m[1] + m[3] + suffix, strings.ReplaceAll(m[2], "-", ".")
	}
	return model, ""
}

// ModelsLine is the Models provider line ("family:line") and model version,
// normalising provider identities across today's harness ids.
func ModelsLine(family, harness, model string) (line, version string) {
	if family == "anthropic" {
		harness, model = "claude", strings.TrimPrefix(model, "anthropic/")
	}
	if family == "openai" && strings.HasPrefix(model, "gpt-") {
		harness = "codex"
	}
	line, version = ModelLine(harness, model)
	return family + ":" + line, version
}

// CompareModelVersions compares dot segments without floating point rounding.
// Aliases follow the CLI's newest version and rank above concrete versions.
func CompareModelVersions(a, b string) int {
	if a == b {
		return 0
	}
	if a == "alias" {
		return 1
	}
	if b == "alias" {
		return -1
	}
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(aa) || i < len(bb); i++ {
		av, bv := 0, 0
		if i < len(aa) {
			av, _ = strconv.Atoi(aa[i])
		}
		if i < len(bb) {
			bv, _ = strconv.Atoi(bb[i])
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	return 0
}

// RouteProfile is one registry profile of a Models line as route selection
// sees it. Active is enabled and not retired.
type RouteProfile struct {
	route, version, effort string
	level                  *int
	active                 bool
}

func NewRouteProfile(family, harness, model, effort string, level *int, active bool) RouteProfile {
	line, version := ModelsLine(family, harness, model)
	route := harness + ":" + model
	// Cursor effort suffixes represent one native route.
	if harness == "cursor" && family == "xai" {
		route = harness + ":" + line
	}
	return RouteProfile{route: route, version: version, effort: effort, level: level, active: active}
}

// RouteCandidates returns the indexes of a line's profiles a step may use:
// only the latest active model version (aliases follow it), and on each route
// the step's effort, else the nearest level to the line's named effort, ties
// up. Reviews take each route's highest native level. Dispatch and the
// account picker share this choice, so a picker default is never a version or
// effort dispatch would not run.
func RouteCandidates(ps []RouteProfile, effort string, level int, review bool) []int {
	for _, p := range ps {
		if effort != "" && p.effort == effort && p.level != nil {
			level = *p.level
			break
		}
	}
	latest := ""
	for _, p := range ps {
		if p.active && p.version != "alias" && CompareModelVersions(p.version, latest) > 0 {
			latest = p.version
		}
	}
	routes := [][]int{}
	keys := map[string]int{}
	for i, p := range ps {
		if p.version != "alias" && p.version != latest {
			continue
		}
		r, ok := keys[p.route]
		if !ok {
			r = len(routes)
			keys[p.route] = r
			routes = append(routes, nil)
		}
		routes[r] = append(routes[r], i)
	}
	out := []int{}
	for _, route := range routes {
		levels := []int{}
		exact := false
		for _, i := range route {
			p := ps[i]
			if !p.active {
				continue
			}
			if p.level != nil {
				levels = append(levels, *p.level)
			}
			exact = exact || effort != "" && p.effort == effort
		}
		nearest := NearestEffort(level, levels)
		if review && ReviewHighestNative {
			nearest = -1
			for _, v := range levels {
				nearest = max(nearest, v)
			}
			exact = false
		}
		for _, i := range route {
			p := ps[i]
			if exact && p.effort != effort || !exact && nearest >= 0 && (p.level == nil || *p.level != nearest) {
				continue
			}
			out = append(out, i)
		}
	}
	return out
}
