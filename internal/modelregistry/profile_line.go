// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"regexp"
	"strconv"
	"strings"
)

var codexLineRE = regexp.MustCompile(`^gpt-([0-9]+(?:\.[0-9]+)*)-(.+)$`)
var concreteLineRE = regexp.MustCompile(`^([a-z][a-z-]*?)-([0-9]+(?:[.-][0-9]+)*)(.*)$`)
var cursorEffortRE = regexp.MustCompile(`^(.+?)(-(low|medium|high|xhigh|max|ultra))?(-fast)?$`)

// ProfileLine derives vendor line and numeric version from immutable model
// ids. Catalog revision (Profile.Version) is deliberately unrelated.
func ProfileLine(p Profile) (family, line, version string) {
	family = p.Family
	id := p.Model
	if p.Harness == "codex" {
		if m := codexLineRE.FindStringSubmatch(id); m != nil {
			return family, m[2], m[1]
		}
		return family, id, ""
	}
	if p.Harness == "pi" {
		id = strings.TrimPrefix(id, "anthropic/")
	}
	if p.Harness == "claude" || p.Harness == "pi" {
		id = strings.TrimPrefix(id, "claude-")
		switch id {
		case "opus", "sonnet", "haiku", "fable":
			return family, id, "alias"
		}
	}
	suffix := ""
	if p.Harness == "cursor" {
		if m := cursorEffortRE.FindStringSubmatch(id); m != nil {
			id = m[1]
			suffix = m[4]
		}
	}
	if m := concreteLineRE.FindStringSubmatch(id); m != nil {
		return family, m[1] + m[3] + suffix, strings.ReplaceAll(m[2], "-", ".")
	}
	return family, p.Model, ""
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
