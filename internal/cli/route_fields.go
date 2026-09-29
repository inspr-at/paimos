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

// Drop provenance for a value this command sets. The server attributes it.
func applyRouteFields(fields map[string]any, role, area string) {
	if role = strings.TrimSpace(role); role != "" {
		fields["route_role"] = role
		delete(fields, "route_role_source")
		delete(fields, "route_role_by")
		delete(fields, "route_role_at")
	}
	if area = strings.TrimSpace(area); area != "" {
		fields["area"] = area
		delete(fields, "area_source")
		delete(fields, "area_by")
		delete(fields, "area_at")
	}
}

func routeProvenance(value, source string) string {
	if source == "" {
		return value
	}
	return value + " (" + source + ")"
}
