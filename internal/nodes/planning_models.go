// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/jackc/pgx/v5"
)

type planningModel struct {
	Provider string `json:"provider,omitempty"`
	modelregistry.Display
	Label    string                 `json:"label"`
	Harness  string                 `json:"harness"`
	Model    string                 `json:"model"`
	Sessions []planningModelSession `json:"sessions"`
}
type planningModelSession struct {
	EffortLevel *int    `json:"effort_level"`
	ID          string  `json:"id"`
	ProfileID   *string `json:"profile_id,omitempty"`
	Raw         *string `json:"model_raw,omitempty"`
	Effort      string  `json:"effort"`
	Role        string  `json:"role"`
	Running     bool    `json:"running"`
	Tokens      *int64  `json:"tokens"`
}

// Read session identity once per page, independently of billing. Match the
// usage subtree and source visibility; unknown models still count as running.
func loadPlanningModels(ctx context.Context, tx pgx.Tx, ids []string) (map[string][]planningModel, map[string]int, error) {
	rows, err := tx.Query(ctx, `WITH `+planningStatesCTE()+`
    SELECT t.root::text, s.id::text, s.harness,
        coalesce(nullif(pk.model,''),nullif(s.model,''),nullif(s.model_raw,''),usage.model,''),
        s.model_profile_id::text, s.model_raw, coalesce(s.reasoning_effort,''), s.role,
        s.stopped_at IS NULL, usage.tokens,
        coalesce(d.provider,''),coalesce(d.model_display->>'display_name',''),coalesce(d.model_display->>'short_name',''),coalesce(d.model_display->>'model_version',''),
        CASE WHEN lower(btrim(s.reasoning_effort))=lower(btrim(p.effort)) THEN d.effort_level END
    FROM (`+planningSubtreeSQL(`SELECT unnest($1::uuid[]) AS root`)+`) t
    JOIN harness_sessions s ON s.tenant_id=current_setting('aeon.tenant_id')::uuid AND s.ticket_node_id=t.id
        AND ((SELECT aeon_visible_all()) OR s.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    LEFT JOIN model_profiles p ON p.tenant_id=s.tenant_id AND p.id=s.model_profile_id
    LEFT JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id
    LEFT JOIN LATERAL aeon_session_model_key(p.model,p.effort) pk ON true
    LEFT JOIN LATERAL (
        SELECT min(u.model) AS model,
            sum(u.input_tokens+u.output_tokens) FILTER (WHERE u.input_tokens IS NOT NULL AND u.output_tokens IS NOT NULL AND u.cached_input_tokens IS NOT NULL)::bigint AS tokens
        FROM harness_session_usage u WHERE u.tenant_id=s.tenant_id AND u.session_id=s.id
    ) usage ON true
    ORDER BY t.root,s.id`, ids)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	grouped := map[string]map[routeKey]*planningModel{}
	running := map[string]int{}
	for rows.Next() {
		var root, harness, model, provider string
		var display modelregistry.Display
		var session planningModelSession
		if err := rows.Scan(&root, &session.ID, &harness, &model, &session.ProfileID, &session.Raw, &session.Effort, &session.Role, &session.Running, &session.Tokens, &provider, &display.DisplayName, &display.ShortName, &display.ModelVersion, &session.EffortLevel); err != nil {
			return nil, nil, err
		}
		if session.Running {
			running[root]++
		}
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if grouped[root] == nil {
			grouped[root] = map[routeKey]*planningModel{}
		}
		key := routeKey{harness: harness, model: model}
		// AEON-503b canonicalises Anthropic IDs to aliases. Keep their declared
		// model versions distinct; an unversioned alias remains unknown.
		if model == "opus" || model == "sonnet" || model == "haiku" || model == "fable" {
			key.effort = display.ModelVersion
		}
		m := grouped[root][key]
		if m == nil {
			m = &planningModel{Provider: provider, Display: display, Harness: harness, Model: model, Label: (modelregistry.Profile{Harness: harness, Model: model}).Label()}
			grouped[root][key] = m
		}
		if m.DisplayName == "" && display.DisplayName != "" {
			m.Display = display
			m.Provider = provider
		}
		m.Sessions = append(m.Sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	out := map[string][]planningModel{}
	for root, models := range grouped {
		for _, model := range models {
			// The largest measured session supplies the meter; ties use the session id.
			sort.Slice(model.Sessions, func(i, j int) bool {
				a, b := model.Sessions[i], model.Sessions[j]
				x, y := int64(-1), int64(-1)
				if a.Tokens != nil {
					x = *a.Tokens
				}
				if b.Tokens != nil {
					y = *b.Tokens
				}
				if x != y {
					return x > y
				}
				return a.ID < b.ID
			})
			out[root] = append(out[root], *model)
		}
		sort.Slice(out[root], func(i, j int) bool {
			a, b := out[root][i], out[root][j]
			x, y := planningModelTokens(a), planningModelTokens(b)
			if x != y {
				return x > y
			}
			if a.Harness != b.Harness {
				return a.Harness < b.Harness
			}
			if a.Model != b.Model {
				return a.Model < b.Model
			}
			return a.ModelVersion < b.ModelVersion
		})
	}
	return out, running, nil
}

func planningModelTokens(m planningModel) int64 {
	var total int64
	for _, s := range m.Sessions {
		if s.Tokens != nil {
			total += *s.Tokens
		}
	}
	return total
}
