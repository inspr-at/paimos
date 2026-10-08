// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type preferenceLevelWrite struct {
	Revision        *int64           `json:"revision"`
	Residency       *string          `json:"residency"`
	ResidencyLocked bool             `json:"residency_locked"`
	PrefsLocked     bool             `json:"prefs_locked"`
	Rows            *[]preferenceRow `json:"rows"`
}
type preferenceRowWrite struct {
	Revision *int64           `json:"revision"`
	Locked   bool             `json:"locked"`
	Normal   *modelprefs.Cell `json:"normal"`
	Complex  *modelprefs.Cell `json:"complex"`
}
type preferenceWriteResult struct {
	PersonID       *string                    `json:"person_id"`
	Level          preferenceLevel            `json:"level"`
	Revision       int64                      `json:"revision"`
	RunningOutside []string                   `json:"running_outside"`
	Residency      modelprefs.ResidencyResult `json:"residency"`
}

var preferenceLineRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func validateSelector(ctx context.Context, tx pgx.Tx, c modelprefs.Cell, kind string) error {
	if c.Mode == "auto" {
		if c.ProfileID != "" || c.Family != "" || c.Line != "" || c.Effort != "" || c.Harness != "" {
			return prefFail(422, "invalid_selector")
		}
		return nil
	}
	if c.Mode == "pinned" {
		if !uuidRE.MatchString(c.ProfileID) || c.Family != "" || c.Line != "" || c.Effort != "" || c.Harness != "" {
			return prefFail(422, "invalid_selector")
		}
	} else if c.Mode == "latest" {
		if c.ProfileID != "" || !validFamily(c.Family) || !preferenceLineRE.MatchString(c.Line) || len(c.Effort) > 32 || !effortRE.MatchString(c.Effort) || (c.Harness != "" && !validHarness(c.Harness)) {
			return prefFail(422, "invalid_selector")
		}
	} else {
		return prefFail(422, "invalid_selector")
	}
	// Only existence is needed. Never decode the catalog or the review ladder.
	// Keep the line expression equivalent to ProfileLine; parity is tested against
	// all catalog harnesses and the concrete/alias/suffix forms.
	var exists, review bool
	err := tx.QueryRow(ctx, `
 WITH candidates AS NOT MATERIALIZED (
   SELECT p.id,p.harness,p.effort,p.tier,
     CASE WHEN p.harness='opencode' THEN regexp_replace(regexp_replace(p.model,'^google/',''),'^ollama/','')
          WHEN p.harness='pi' THEN regexp_replace(p.model,'^anthropic/','')
          ELSE p.model END AS model
   FROM model_profiles p
   JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id
   WHERE ($1='pinned' AND p.id=NULLIF($2,'')::uuid)
      OR ($1='latest' AND p.family=$3 AND p.effort=$5 AND ($6='' OR p.harness=$6))
 ), normalized AS NOT MATERIALIZED (
   SELECT *, CASE WHEN harness IN ('claude','pi') THEN regexp_replace(model,'^claude-','')
                  ELSE model END AS normalized_model FROM candidates
 ), parts AS NOT MATERIALIZED (
   SELECT *, regexp_match(normalized_model,'^(.+?)(-(low|medium|high|xhigh|max|ultra))?(-fast)?$') AS cursor_parts
   FROM normalized
 ), lines AS NOT MATERIALIZED (
   SELECT *,
     regexp_match(CASE WHEN harness='cursor' THEN cursor_parts[1] ELSE normalized_model END,
                  '^([a-z][a-z-]*?)-?([0-9]+(?:[.-][0-9]+)*)(.*)$') AS concrete_parts,
     regexp_match(normalized_model,'^gpt-([0-9]+(?:\.[0-9]+)*)-(.+)$') AS codex_parts
   FROM parts
 ), matches AS NOT MATERIALIZED (
   SELECT * FROM lines WHERE $1='pinned' OR
     (CASE WHEN harness='codex' THEN codex_parts[2]
           WHEN harness IN ('claude','pi') AND normalized_model IN ('opus','sonnet','haiku','fable') THEN normalized_model
           ELSE concrete_parts[1]||concrete_parts[3]||CASE WHEN harness='cursor' THEN coalesce(cursor_parts[4],'') ELSE '' END
      END)=$4
 )
 SELECT EXISTS(SELECT 1 FROM matches LIMIT 1),
        CASE WHEN $7 THEN EXISTS(SELECT 1 FROM matches p
          WHERE p.effort='xhigh' AND p.tier IN ('strong','frontier')
            AND EXISTS(SELECT 1 FROM model_role_routes r WHERE r.profile_id=p.id AND r.role='review-gate' LIMIT 1)
          LIMIT 1) ELSE true END`, c.Mode, c.ProfileID, c.Family, c.Line, c.Effort, c.Harness, kind == "review").Scan(&exists, &review)
	if err != nil {
		return err
	}
	if !exists {
		if c.Mode == "pinned" {
			return prefFail(422, "unknown_profile")
		}
		return prefFail(422, "unknown_line")
	}
	if !review {
		return prefFail(422, "review_floor")
	}
	return nil
}

type selectorCheck struct {
	Cell   modelprefs.Cell
	Review bool
}
type selectorChecks map[selectorCheck]bool

func (checks selectorChecks) validate(ctx context.Context, tx pgx.Tx, cell modelprefs.Cell, kind string) error {
	key := selectorCheck{cell, kind == "review"}
	if checks[key] {
		return nil
	}
	if err := validateSelector(ctx, tx, cell, kind); err != nil {
		return err
	}
	checks[key] = true
	return nil
}

func preferenceKind(ctx context.Context, tx pgx.Tx, id, level, project string) (modelprefs.Kind, error) {
	var k modelprefs.Kind
	if !uuidRE.MatchString(id) {
		return k, prefFail(422, "unknown_kind")
	}
	err := tx.QueryRow(ctx, `SELECT id::text,slug,label,hint,project_id::text,system,position FROM work_kinds WHERE id=$1::uuid AND archived_at IS NULL`, id).Scan(&k.ID, &k.Slug, &k.Label, &k.Hint, &k.ProjectID, &k.System, &k.Position)
	if err == pgx.ErrNoRows {
		return k, prefFail(422, "unknown_kind")
	}
	if err != nil {
		return k, err
	}
	if k.ProjectID != nil && (level != "project" || *k.ProjectID != project) {
		return k, prefFail(422, "kind_not_in_project")
	}
	return k, nil
}
func unlockedRow(chain []modelprefs.Scope, index int, kind string) error {
	for _, s := range chain[:index] {
		if s.PrefsLocked || s.Rows[kind].Locked {
			return prefFail(422, "locked_above")
		}
	}
	return nil
}
func writeRow(ctx context.Context, tx pgx.Tx, p tenant.Principal, scope modelprefs.Scope, k modelprefs.Kind, row preferenceRow, checks selectorChecks) error {
	if scope.Level == "project" && row.Locked {
		return prefFail(422, "lock_at_project")
	}
	for _, cell := range []modelprefs.Cell{row.Normal, row.Complex} {
		if err := checks.validate(ctx, tx, cell, k.Slug); err != nil {
			return err
		}
	}
	return modelprefs.PutRow(ctx, tx, p, scope, k.ID, modelprefs.Row{Locked: row.Locked, Cells: map[string]modelprefs.Cell{"normal": row.Normal, "complex": row.Complex}})
}
func (m *Module) writePreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	level := r.PathValue("level")
	index := levelIndex(level)
	if index < 0 {
		writePreferenceError(w, prefFail(400, "invalid_level"))
		return
	}
	project, err := projectInput(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	if err := workBody(w, r); err != nil {
		writePreferenceError(w, err)
		return
	}
	rowID := r.PathValue("kindId")
	var in preferenceLevelWrite
	var row preferenceRowWrite
	var revision int64
	if r.Method == http.MethodDelete {
		revision, err = requestedRevision(r)
	} else if rowID != "" {
		err = decodeJSON(w, r, &row)
		if err == nil {
			if row.Revision == nil || *row.Revision < 0 {
				err = prefFail(400, "revision_required")
			} else {
				revision = *row.Revision
			}
		}
	} else {
		err = decodeJSON(w, r, &in)
		if err == nil {
			if in.Revision == nil || *in.Revision < 0 {
				err = prefFail(400, "revision_required")
			} else {
				revision = *in.Revision
			}
		}
		if err == nil && (in.Residency != nil && *in.Residency != "any" && *in.Residency != "eu" && *in.Residency != "local" || in.ResidencyLocked && in.Residency == nil) {
			err = prefFail(422, "invalid_residency")
		}
		if err == nil && in.Rows != nil && len(*in.Rows) > maxPreferenceKinds {
			err = prefFail(413, "too_many_rows")
		}
	}
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	var out preferenceWriteResult
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := preferenceFence(ctx, tx, p); err != nil {
			return err
		}
		if err := workorders.RefreshPrincipal(ctx, tx, p); err != nil {
			return err
		}
		person, err := currentPreferencePerson(ctx, tx, p)
		if err != nil {
			return err
		}
		if err := authorizePreference(ctx, tx, p, level, project, person); err != nil {
			return err
		}
		if project != "" {
			if err := readableProject(ctx, tx, p, project); err != nil {
				return err
			}
		}
		expected, err := expectedPreferencePerson(r, level)
		if err != nil {
			return err
		}
		if expected != "" && (person == nil || *person != expected) {
			return prefFail(409, "preference_person_changed")
		}
		out.PersonID = person
		kinds, err := visibleKinds(ctx, tx, project)
		if err != nil {
			return err
		}
		chain, err := modelprefs.LoadChain(ctx, tx, person, project)
		if err != nil {
			return err
		}
		scope := chain[index]
		if scope.Revision != revision {
			return prefFail(409, "stale_revision")
		}
		before, err := levelResponse(ctx, tx, scope, kinds)
		if err != nil {
			return err
		}
		scope.Level = level
		scope.PersonID = nil
		scope.ProjectID = nil
		if level == "person" {
			scope.PersonID = person
		}
		if level == "project" {
			scope.ProjectID = &project
		}
		checks := selectorChecks{}
		replacementKinds := map[string]modelprefs.Kind{}
		var kind *modelprefs.Kind
		if rowID != "" {
			k, err := preferenceKind(ctx, tx, rowID, level, project)
			if err != nil {
				return err
			}
			kind = &k
			if r.Method != http.MethodDelete {
				if err := unlockedRow(chain, index, k.Slug); err != nil {
					return err
				}
				if level == "project" && row.Locked {
					return prefFail(422, "lock_at_project")
				}
				if row.Normal == nil || row.Complex == nil {
					if !row.Locked || row.Normal != nil || row.Complex != nil {
						return prefFail(422, "both_buckets_required")
					}
					// Snapshot the effective selectors for BOTH buckets, at this level.
					normal := modelprefs.ResolveCell(chain[:index+1], k.Slug, "normal")
					complex := modelprefs.ResolveCell(chain[:index+1], k.Slug, "complex")
					a, b := modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"}
					if normal.Cell != nil {
						a = *normal.Cell
					}
					if complex.Cell != nil {
						b = *complex.Cell
					}
					row.Normal = &a
					row.Complex = &b
				}
			}
		} else if r.Method == http.MethodDelete {
			scope.Residency = nil
			scope.ResidencyLocked = false
			scope.PrefsLocked = false
		} else {
			scope.Residency = in.Residency
			scope.ResidencyLocked = in.ResidencyLocked
			scope.PrefsLocked = in.PrefsLocked
			if level == "project" && (scope.ResidencyLocked || scope.PrefsLocked) {
				return prefFail(422, "lock_at_project")
			}
			if in.Rows != nil {
				seen := map[string]bool{}
				for _, rr := range *in.Rows {
					if seen[rr.KindID] {
						return prefFail(422, "duplicate_kind")
					}
					seen[rr.KindID] = true
					k, err := preferenceKind(ctx, tx, rr.KindID, level, project)
					if err != nil {
						return err
					}
					if err := unlockedRow(chain, index, k.Slug); err != nil {
						return err
					}
					if level == "project" && rr.Locked {
						return prefFail(422, "lock_at_project")
					}
					for _, cell := range []modelprefs.Cell{rr.Normal, rr.Complex} {
						if err := checks.validate(ctx, tx, cell, k.Slug); err != nil {
							return err
						}
					}
					replacementKinds[rr.KindID] = k
				}
			}
		}
		var pending []events.Change
		var priorRequirements map[string]string
		if kind == nil && (before.Residency != nil || scope.Residency != nil || before.ResidencyLocked != scope.ResidencyLocked) {
			priorRequirements, err = modelprefs.SnapshotRunRequirements(ctx, tx, scope)
			if err != nil {
				return err
			}
		}
		scope, err = modelprefs.SaveScopeOnly(ctx, tx, p, scope)
		if err != nil {
			return err
		}
		if kind != nil {
			if r.Method == http.MethodDelete {
				_, err = tx.Exec(ctx, `DELETE FROM model_pref_rows WHERE scope_id=$1 AND kind_id=$2`, scope.ID, kind.ID)
			} else {
				err = writeRow(ctx, tx, p, scope, *kind, preferenceRow{kind.ID, row.Locked, *row.Normal, *row.Complex}, checks)
			}
		} else if r.Method == http.MethodDelete || in.Rows != nil {
			_, err = tx.Exec(ctx, `DELETE FROM model_pref_rows WHERE scope_id=$1`, scope.ID)
			if err == nil && r.Method != http.MethodDelete {
				for _, rr := range *in.Rows {
					k := replacementKinds[rr.KindID]
					if err = writeRow(ctx, tx, p, scope, k, rr, checks); err != nil {
						break
					}
				}
			}
		}
		if err != nil {
			return err
		}
		// Bound persisted rows too: repeated individual writes cannot grow a scope
		// beyond the editor's limit or bypass a level-replacement body bound.
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_pref_rows WHERE scope_id=$1`, scope.ID).Scan(&count); err != nil {
			return err
		}
		if count > maxPreferenceKinds {
			return prefFail(413, "too_many_rows")
		}
		// Restamp on a residency change; loosening leaves all stamps untouched.
		out.RunningOutside = []string{}
		if priorRequirements != nil {
			runs, changes, err := modelprefs.RestampAfterDeferred(ctx, tx, scope, priorRequirements)
			if err != nil {
				return err
			}
			pending = append(pending, changes...)
			now, err := dbNow(ctx, tx)
			if err != nil {
				return err
			}
			for _, run := range runs {
				if (run.Status == "starting" || run.Status == "running") && run.AccountID != nil && run.ProfileID != nil {
					meets, err := agentaccounts.AccountMeetsResidency(ctx, tx, *run.AccountID, *run.ProfileID, run.Residency, now)
					if err != nil {
						return err
					}
					if !meets {
						out.RunningOutside = append(out.RunningOutside, run.ID)
					}
				}
			}
		}
		chain, err = modelprefs.LoadChain(ctx, tx, person, project)
		if err != nil {
			return err
		}
		after, err := levelResponse(ctx, tx, chain[index], kinds)
		if err != nil {
			return err
		}
		out.Level = after
		out.Revision = after.Revision
		out.Residency = modelprefs.ResolveResidency(chain[:index+1])
		metadata := map[string]any{"level": level, "scope": scope.ID, "kind": rowID, "before": before, "after": after}
		// Flush only after all mutations, response loading and residency checks.
		for _, change := range pending {
			if _, err := events.Append(ctx, tx, p, change); err != nil {
				return err
			}
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "model.preferences_changed", Before: map[string]any{"level": level, "scope": scope.ID, "kind": rowID, "value": before}, After: metadata})
		return err
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// Count characters after the caller's normalization, matching SQL length.
func boundedText(s string, max int) bool { return len([]rune(s)) <= max }

// The value is an equality precondition only; target selection stays server-side.
func expectedPreferencePerson(r *http.Request, level string) (string, error) {
	values := r.Header.Values("If-Prefs-Person")
	if len(values) == 0 {
		if level == "person" {
			return "", prefFail(428, "person_precondition_required")
		}
		return "", nil
	}
	if len(values) != 1 || len(values[0]) != 36 || !uuidRE.MatchString(values[0]) {
		return "", prefFail(400, "invalid_person_precondition")
	}
	return strings.ToLower(values[0]), nil
}
