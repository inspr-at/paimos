// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type catalogRouteView struct {
	Route string                 `json:"route"`
	Facts map[string]CatalogFact `json:"facts"`
}
type catalogModelView struct {
	Identity  string                 `json:"identity"`
	Provider  string                 `json:"provider"`
	ModelID   string                 `json:"model_id"`
	Facts     map[string]CatalogFact `json:"facts"`
	Routes    []catalogRouteView     `json:"routes"`
	Evidence  []CatalogFact          `json:"evidence"`
	Truncated bool                   `json:"truncated"`
}
type catalogPage struct {
	Models     []catalogModelView `json:"models"`
	NextCursor string             `json:"next_cursor"`
	Truncated  bool               `json:"truncated"`
}

func (m *Module) catalog(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.authorized(r, p, "models.read"); err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 50 {
			writeErr(w, fail(400, "catalog limit must be from 1 to 50"))
			return
		}
		limit = n
	}
	after := []string{"", ""}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		if len(cursor) > 512 {
			writeErr(w, fail(400, "invalid catalog cursor"))
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &after) != nil || len(after) != 2 || len(after[0]) > 64 || len(after[1]) > 128 || !modelRE.MatchString(after[0]) || !modelRE.MatchString(after[1]) {
			writeErr(w, fail(400, "invalid catalog cursor"))
			return
		}
	}
	if err := PrepareCatalog(r.Context(), m.pool, p, CatalogPreparation{Operation: CatalogRead, Request: r}); err != nil {
		writeErr(w, err)
		return
	}

	out := catalogPage{Models: []catalogModelView{}}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := preferenceFence(r.Context(), tx, p); err != nil {
			return err
		}
		current, err := workorders.CurrentKeyPrincipal(r, tx, p, "models.read")
		if err != nil {
			return err
		}
		if err := authz.RequireTx(r.Context(), tx, current, "models.read", authz.Scope{}); err != nil {
			return err
		}

		rows, err := tx.Query(r.Context(), `SELECT DISTINCT provider,model_id FROM model_catalog_facts WHERE (provider,model_id)>($1,$2) ORDER BY provider,model_id LIMIT $3`, after[0], after[1], limit+1)
		if err != nil {
			return err
		}
		ids := [][2]string{}
		for rows.Next() {
			var id [2]string
			if err := rows.Scan(&id[0], &id[1]); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) > limit {
			ids = ids[:limit]
			raw, _ := json.Marshal(ids[len(ids)-1])
			out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		}
		remaining := 4 << 20
		for _, id := range ids {
			model := catalogModelView{Identity: id[0] + ":" + id[1], Provider: id[0], ModelID: id[1], Evidence: []CatalogFact{}, Routes: []catalogRouteView{}}
			// The evidence count and byte budget also cover future harness sources.
			rows, err := tx.Query(r.Context(), `SELECT provider,model_id,route,fact,value,source,source_kind,field_path,fetched_at FROM model_catalog_facts WHERE provider=$1 AND model_id=$2 ORDER BY CASE source_kind WHEN 'harness' THEN 3 WHEN 'native' THEN 2 WHEN 'aggregator' THEN 1 ELSE 0 END DESC,source,route,fact LIMIT 257`, id[0], id[1])
			if err != nil {
				return err
			}
			for rows.Next() {
				var f CatalogFact
				if err := rows.Scan(&f.Provider, &f.ModelID, &f.Route, &f.Fact, &f.Value, &f.Source, &f.SourceKind, &f.FieldPath, &f.FetchedAt); err != nil {
					rows.Close()
					return err
				}
				size := len(f.Value) + len(f.Source) + len(f.Route) + len(f.FieldPath) + 256
				if len(model.Evidence) >= 256 || size > remaining {
					model.Truncated = true
					out.Truncated = true
					continue
				}
				remaining -= size
				model.Evidence = append(model.Evidence, f)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			model.Facts = ResolveCatalogFacts(model.Evidence, "")
			routes := map[string]bool{}
			for _, f := range model.Evidence {
				if f.Route != "" {
					routes[f.Route] = true
				}
			}
			keys := make([]string, 0, len(routes))
			for route := range routes {
				keys = append(keys, route)
			}
			sort.Strings(keys)
			for _, route := range keys {
				model.Routes = append(model.Routes, catalogRouteView{route, ResolveCatalogFacts(model.Evidence, route)})
			}
			// Unknown is explicit even for an offline-only ID.
			for _, name := range []string{"display_name", "native_levels", "default_level", "lifecycle", "retirement_at", "successor"} {
				if _, ok := model.Facts[name]; !ok {
					model.Facts[name] = CatalogFact{Provider: id[0], ModelID: id[1], Fact: name, Value: json.RawMessage("null")}
				}
			}
			out.Models = append(out.Models, model)
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
