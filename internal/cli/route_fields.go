// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"strings"

	"github.com/inspr-at/paimos/internal/modelregistry"
)

func validateRouteFlags(role, area, complexity string) error {
	if strings.TrimSpace(role) != "" && !modelregistry.KnownRouteRole(role) {
		return usagef("--role must be scout, mechanical, build, build-hard, or review-gate")
	}
	complexity = strings.TrimSpace(complexity)
	if complexity != "" && complexity != "S" && complexity != "M" && complexity != "L" {
		return usagef("--complexity must be S, M, or L")
	}
	return nil
}

// applyRouteFields sets route_role, area and complexity. An unchanged value keeps its
// stored provenance. A new value drops provenance so the server re-stamps it.
func applyRouteFields(fields map[string]any, role, area, complexity string) {
	assignRouteValue(fields, "route_role", "route_role_source", "route_role_by", "route_role_at", role)
	assignRouteValue(fields, "area", "area_source", "area_by", "area_at", area)
	assignRouteValue(fields, "complexity", "complexity_source", "complexity_by", "complexity_at", complexity)
}

func assignRouteValue(fields map[string]any, valueKey, sourceKey, byKey, atKey, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	unconfirmed := fields[sourceKey] == "suggested" || fields[sourceKey] == "agent" && fields[valueKey+"_confirmed"] == false
	if current, ok := fields[valueKey].(string); ok && strings.TrimSpace(current) == value && !unconfirmed {
		fields[valueKey] = strings.TrimSpace(current)
		return
	}
	fields[valueKey] = value
	delete(fields, sourceKey)
	delete(fields, byKey)
	delete(fields, atKey)
	delete(fields, valueKey+"_confirmed")
}

func routeProvenance(value, source string) string {
	if source == "" {
		return value
	}
	return value + " (" + source + ")"
}
