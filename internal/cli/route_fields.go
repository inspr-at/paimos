// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"strings"

	"github.com/inspr-at/paimos/internal/modelregistry"
)

func validateRouteFlags(role, area string) error {
	if strings.TrimSpace(role) != "" && !modelregistry.KnownRouteRole(role) {
		return usagef("--role must be scout, mechanical, build, build-hard, or review-gate")
	}
	if strings.TrimSpace(area) != "" && !modelregistry.KnownRouteArea(area) {
		return usagef("--area must be backend, frontend, full-stack, infra, design, or docs")
	}
	return nil
}

// applyRouteFields sets route_role and area. An unchanged value keeps its
// stored provenance. A new value drops provenance so the server re-stamps it.
func applyRouteFields(fields map[string]any, role, area string) {
	assignRouteValue(fields, "route_role", "route_role_source", "route_role_by", "route_role_at", role)
	assignRouteValue(fields, "area", "area_source", "area_by", "area_at", area)
}

func assignRouteValue(fields map[string]any, valueKey, sourceKey, byKey, atKey, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if current, ok := fields[valueKey].(string); ok && strings.TrimSpace(current) == value {
		fields[valueKey] = strings.TrimSpace(current)
		return
	}
	fields[valueKey] = value
	delete(fields, sourceKey)
	delete(fields, byKey)
	delete(fields, atKey)
}

func routeProvenance(value, source string) string {
	if source == "" {
		return value
	}
	return value + " (" + source + ")"
}
