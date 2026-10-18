// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import "github.com/inspr-at/paimos/internal/modelprefs"

// ProfileLine derives vendor line and numeric version from immutable model
// ids. Catalog revision (Profile.Version) is deliberately unrelated.
func ProfileLine(p Profile) (family, line, version string) {
	line, version = modelprefs.ModelLine(p.Harness, p.Model)
	return p.Family, line, version
}

// CompareModelVersions compares dot segments without floating point rounding.
// Aliases follow the CLI's newest version and rank above concrete versions.
func CompareModelVersions(a, b string) int {
	return modelprefs.CompareModelVersions(a, b)
}
