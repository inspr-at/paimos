// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type rulesDocument struct {
	Rules    []modelprefs.Rule `json:"rules"`
	Revision int64             `json:"revision"`
}

func ruleRevision(ctx context.Context, tx pgx.Tx, scope, project string) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx, `SELECT revision FROM model_rule_revisions WHERE scope=$1 AND scope_key=$2`, scope, project).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return revision, err
}
func (m *Module) modelRules(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project, err := projectInput(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	var out rulesDocument
	err = m.readSnapshot(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		current, err := currentModelReader(r, tx, p)
		if err != nil {
			return err
		}
		if err := readableProject(r.Context(), tx, current, project); err != nil {
			return err
		}
		s, err := modelprefs.LoadBoard(r.Context(), tx, nil, project)
		if err != nil {
			return err
		}
		out.Rules = s.Rules
		scope := "workspace"
		if project != "" {
			scope = "project"
		}
		out.Revision, err = ruleRevision(r.Context(), tx, scope, project)
		return err
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

type rulePin struct {
	Line string `json:"line"`
	Why  string `json:"why"`
}
type rulesWrite struct {
	Top      []rulePin         `json:"top"`
	Bottom   []rulePin         `json:"bottom"`
	Not      map[string]string `json:"not"`
	Revision *int64            `json:"revision"`
}

func (m *Module) writeModelRules(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	project, err := projectInput(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	scope, column := r.PathValue("scope"), r.PathValue("column")
	if scope != "workspace" && scope != "project" || scope == "project" && project == "" || scope == "workspace" && project != "" || len(column) > 64 {
		writePreferenceError(w, prefFail(400, "invalid_rule_scope"))
		return
	}
	if err := workBody(w, r); err != nil {
		writePreferenceError(w, err)
		return
	}
	var in rulesWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writePreferenceError(w, err)
		return
	}
	if in.Top == nil || in.Bottom == nil || in.Not == nil {
		writePreferenceError(w, prefFail(422, "rule_zones_required"))
		return
	}
	if len(in.Top)+len(in.Bottom)+len(in.Not) > 512 {
		writePreferenceError(w, prefFail(413, "too_many_rules"))
		return
	}
	var out rulesDocument
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		level := "default"
		if scope == "project" {
			level = "project"
		}
		if _, err := boardWriteAuthority(ctx, tx, r, p, level, project); err != nil {
			return err
		}
		s, err := modelprefs.LoadBoard(ctx, tx, nil, project)
		if err != nil {
			return err
		}
		if !validBoardColumn(s, column) {
			return prefFail(422, "unknown_column")
		}
		revision, err := ruleRevision(ctx, tx, scope, project)
		if err != nil {
			return err
		}
		if in.Revision == nil || *in.Revision < 0 {
			return prefFail(400, "revision_required")
		}
		if revision != *in.Revision {
			return prefFail(409, "stale_revision")
		}
		c, err := loadBoardCatalog(ctx, tx)
		if err != nil {
			return err
		}
		desired := []modelprefs.Rule{}
		seen := map[string]string{}
		add := func(line, why, lock string, position int) error {
			if len(line) > 128 || seen[line] != "" || strings.TrimSpace(why) == "" || !boundedText(why, 1000) {
				return prefFail(422, "invalid_rule")
			}
			if _, ok := c.profiles[line]; !ok {
				return prefFail(422, "unknown_line")
			}
			seen[line] = lock
			desired = append(desired, modelprefs.Rule{Scope: scope, Column: column, Line: line, Lock: lock, Why: strings.TrimSpace(why), Position: position, SetBy: &p.ID})
			return nil
		}
		for i, pin := range in.Top {
			if err := add(pin.Line, pin.Why, "top", i); err != nil {
				return err
			}
		}
		for i, pin := range in.Bottom {
			if err := add(pin.Line, pin.Why, "bottom", i); err != nil {
				return err
			}
		}
		keys := []string{}
		for line := range in.Not {
			keys = append(keys, line)
		}
		sort.Strings(keys)
		for i, line := range keys {
			if err := add(line, in.Not[line], "not", i); err != nil {
				return err
			}
		}
		inherited := map[string]bool{}
		if scope == "project" {
			for _, rule := range s.Rules {
				if rule.Scope != "workspace" || rule.Column != column {
					continue
				}
				if seen[rule.Line] == "" || seen[rule.Line] != rule.Lock && seen[rule.Line] != "not" {
					return prefFail(422, "looser_than_workspace")
				}
				if seen[rule.Line] == rule.Lock {
					inherited[rule.Line] = true
				}
			}
		}
		desired = slices.DeleteFunc(desired, func(rule modelprefs.Rule) bool { return inherited[rule.Line] })
		before := []modelprefs.Rule{}
		for _, rule := range s.Rules {
			if rule.Scope == scope && rule.Column == column {
				before = append(before, rule)
			}
		}
		if len(s.Rules)-len(before)+len(desired) > 4096 {
			return modelprefs.ErrBoardBounds
		}
		if _, err := tx.Exec(ctx, `DELETE FROM model_rules WHERE scope=$1 AND project_id IS NOT DISTINCT FROM $2::uuid AND column_key=$3`, scope, optionalUUID(project), column); err != nil {
			return err
		}
		for _, rule := range desired {
			if _, err := tx.Exec(ctx, `INSERT INTO model_rules(tenant_id,scope,project_id,column_key,line,lock,position,why,set_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.TenantID, scope, optionalUUID(project), column, rule.Line, rule.Lock, rule.Position, rule.Why, p.ID); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO model_rule_revisions(tenant_id,scope,scope_key,revision) VALUES($1,$2,$3,1) ON CONFLICT(tenant_id,scope,scope_key) DO UPDATE SET revision=model_rule_revisions.revision+1 RETURNING revision`, p.TenantID, scope, project).Scan(&out.Revision); err != nil {
			return err
		}
		after, err := modelprefs.LoadBoard(ctx, tx, nil, project)
		if err != nil {
			return err
		}
		out.Rules = after.Rules
		_, err = events.Append(ctx, tx, p, events.Change{Type: "model.rules_changed", Before: map[string]any{"scope": scope, "project_id": optionalUUID(project), "column": column, "rules": before}, After: out})
		return err
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) situationLimits(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var out modelprefs.SituationLimits
	err := m.readSnapshot(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if _, err := currentModelReader(r, tx, p); err != nil {
			return err
		}
		var err error
		out, err = modelprefs.LoadSituationLimits(r.Context(), tx)
		return err
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) writeSituationLimits(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	var in struct {
		SmallHours int    `json:"small_hours"`
		FixRounds  int    `json:"fix_rounds"`
		Revision   *int64 `json:"revision"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writePreferenceError(w, err)
		return
	}
	if in.SmallHours < 1 || in.SmallHours > 8 || in.FixRounds < 1 || in.FixRounds > 6 {
		writePreferenceError(w, prefFail(422, "invalid_situation_limits"))
		return
	}
	var out modelprefs.SituationLimits
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if _, err := boardWriteAuthority(ctx, tx, r, p, "default", ""); err != nil {
			return err
		}
		before, err := modelprefs.LoadSituationLimits(ctx, tx)
		if err != nil {
			return err
		}
		if in.Revision == nil || *in.Revision < 0 {
			return prefFail(400, "revision_required")
		}
		if *in.Revision != before.Revision {
			return prefFail(409, "stale_revision")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_situation_limits(tenant_id,small_hours,fix_rounds,revision,set_by) VALUES($1,$2,$3,1,$4) ON CONFLICT(tenant_id) DO UPDATE SET small_hours=excluded.small_hours,fix_rounds=excluded.fix_rounds,revision=model_situation_limits.revision+1,set_by=excluded.set_by,set_at=now()`, p.TenantID, in.SmallHours, in.FixRounds, p.ID); err != nil {
			return err
		}
		out, err = modelprefs.LoadSituationLimits(ctx, tx)
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "model.situations_changed", Before: before, After: out})
		return err
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) orderWorkKinds(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	project, err := projectInput(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	var in struct {
		Slugs []string `json:"slugs"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writePreferenceError(w, err)
		return
	}
	if len(in.Slugs) > 256 {
		writePreferenceError(w, prefFail(413, "too_many_kinds"))
		return
	}
	var out workKindPage
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		level := "default"
		if project != "" {
			level = "project"
		}
		if _, err := boardWriteAuthority(ctx, tx, r, p, level, project); err != nil {
			return err
		}
		kinds, err := visibleKinds(ctx, tx, project)
		if err != nil {
			return err
		}
		if len(in.Slugs) != len(kinds) {
			return prefFail(422, "all_kinds_required")
		}
		bySlug := map[string]modelprefs.Kind{}
		for _, k := range kinds {
			bySlug[k.Slug] = k
		}
		seen := map[string]bool{}
		for i, slug := range in.Slugs {
			k, ok := bySlug[slug]
			if !ok || seen[slug] {
				return prefFail(422, "invalid_kind_order")
			}
			seen[slug] = true
			if k.ProjectID == nil && project != "" {
				if err := authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{}); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE work_kinds SET position=$2 WHERE id=$1`, k.ID, i); err != nil {
				return err
			}
		}
		out.Items = []workKind{}
		for _, slug := range in.Slugs {
			k, err := kindScan(ctx, tx, bySlug[slug].ID)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, k)
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "work_kind.ordered", Before: kinds, After: out.Items})
		return err
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
