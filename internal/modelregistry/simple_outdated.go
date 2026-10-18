// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

type simpleOutdatedName struct {
	Line      string `json:"line"`
	Successor string `json:"successor"`
}
type simpleOutdated struct {
	Count int                  `json:"count"`
	Named []simpleOutdatedName `json:"named"`
	Uses  int                  `json:"uses"`
}

// The current catalog exposes registered versions, not provider lifecycle
// facts. Group versions across routes and native levels; name each by one
// immutable profile id. Detailed lifecycle and retirement remain AEON-1145.
func simpleOutdatedFor(ctx context.Context, tx pgx.Tx, person *string, c boardCatalog) (simpleOutdated, error) {
	out := simpleOutdated{Named: []simpleOutdatedName{}}
	latest := map[string]Profile{}
	versions := map[string]Profile{}
	versionIDs := map[string][]string{}
	for _, profiles := range c.profiles {
		for _, profile := range profiles {
			line, version := modelsLine(profile)
			if version == "" || version == "alias" || profile.Retired {
				continue
			}
			key := line + "@" + version
			versionIDs[key] = append(versionIDs[key], profile.ID)
			if previous, ok := versions[key]; !ok || profile.ID < previous.ID {
				versions[key] = profile
			}
			if profile.Enabled {
				previous := latest[line]
				_, old := modelsLine(previous)
				if previous.ID == "" || CompareModelVersions(version, old) > 0 || version == old && profile.ID < previous.ID {
					latest[line] = profile
				}
			}
		}
	}
	keys := make([]string, 0, len(versions))
	for key := range versions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ids := []string{}
	for _, key := range keys {
		old := versions[key]
		line, version := modelsLine(old)
		next := latest[line]
		_, successor := modelsLine(next)
		if next.ID == "" || CompareModelVersions(successor, version) <= 0 {
			continue
		}
		out.Named = append(out.Named, simpleOutdatedName{old.ID, next.ID})
		ids = append(ids, versionIDs[key]...)
	}
	out.Count = len(out.Named)
	if len(ids) == 0 {
		return out, nil
	}
	// Row orders target latest family:line and already use the successor.
	// Count explicit pins only in the workspace and this canonical person's
	// visible scopes. The simple page never reveals other people's usages.
	err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM model_pref_cells c
 JOIN model_pref_scopes s ON s.tenant_id=c.tenant_id AND s.id=c.scope_id
 WHERE c.mode='pinned' AND c.profile_id=ANY($1::uuid[]) AND (s.level='default' OR s.person_id=$2::uuid) LIMIT 10001) uses`, ids, person).Scan(&out.Uses)
	if err == nil && out.Uses > 10000 {
		return out, prefFail(413, "line_usage_limit")
	}
	return out, err
}
