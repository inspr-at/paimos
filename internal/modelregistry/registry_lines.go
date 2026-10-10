// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelactivation"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type lineWrite struct {
	DisplayName string   `json:"display_name"`
	Note        string   `json:"note"`
	Efforts     []string `json:"efforts"`
	Route       string   `json:"route"`
	Model       string   `json:"model"`
	Revision    *string  `json:"revision"`
}

func linePath(r *http.Request) (string, string, error) {
	h, model := r.PathValue("harness"), r.PathValue("model")
	if !validHarness(h) || len(model) > 128 || !modelRE.MatchString(model) {
		return "", "", fail(400, "invalid model line")
	}
	return h, model, nil
}

func lineProfiles(c boardCatalog, h, model string) []Profile {
	out := []Profile{}
	for _, ps := range c.profiles {
		for _, p := range ps {
			if p.Harness == h && p.Model == model && !p.Retired {
				out = append(out, p)
			}
		}
	}
	slices.SortFunc(out, func(a, b Profile) int {
		if a.CreatedAt.Before(b.CreatedAt) {
			return 1
		}
		if a.CreatedAt.After(b.CreatedAt) {
			return -1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// Unlike the role-ladder token, this digest includes all registry pins and
// preference/rule revisions, including lines that no ladder currently uses.
func registryRevision(ctx context.Context, tx pgx.Tx) (string, error) {
	// Limit every contributing set before building the digest.
	var large bool
	if err := tx.QueryRow(ctx, `SELECT
 (SELECT count(*)>512 FROM (SELECT 1 FROM model_profiles LIMIT 513) p) OR
 (SELECT count(*)>512 FROM (SELECT 1 FROM model_profile_retirements LIMIT 513) r) OR
 (SELECT count(*)>4096 FROM (SELECT 1 FROM model_pref_profiles LIMIT 4097) p) OR
 (SELECT count(*)>1536 FROM (SELECT 1 FROM model_rule_revisions LIMIT 1537) r)`).Scan(&large); err != nil {
		return "", err
	}
	if large {
		return "", modelprefs.ErrBoardBounds
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT jsonb_build_array(
 coalesce((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM model_profiles p),'[]'::jsonb),
 coalesce((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.profile_id) FROM model_profile_retirements r),'[]'::jsonb),
 coalesce((SELECT jsonb_agg(jsonb_build_array(p.id,p.revision) ORDER BY p.id) FROM model_pref_profiles p),'[]'::jsonb),
 coalesce((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.scope,r.scope_key) FROM model_rule_revisions r),'[]'::jsonb))`).Scan(&raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validateLineWrite(in lineWrite, h, oldModel string) error {
	if strings.TrimSpace(in.DisplayName) == "" || !boundedText(in.DisplayName, 128) || strings.ContainsAny(in.DisplayName, "\x00\r\n") || !boundedText(in.Note, 80) || strings.ContainsAny(in.Note, "\x00\r\n") {
		return fail(422, "invalid model name or note")
	}
	if len(in.Efforts) < 1 || len(in.Efforts) > 16 {
		return fail(422, "one to sixteen efforts required")
	}
	seen := map[string]bool{}
	for _, e := range in.Efforts {
		if len(e) > 32 || !effortRE.MatchString(e) || seen[e] {
			return fail(422, "invalid or duplicate effort")
		}
		seen[e] = true
	}
	model := in.Model
	if model == "" {
		model = oldModel
	}
	family := harnesslaunch.ModelFamily(h, model)
	provider := strings.ToLower(strings.TrimSpace(in.Route))
	if provider == "" || len(provider) > 32 {
		return fail(422, "invalid provider route")
	}
	expected := map[string]string{"codex": "openai", "claude": "anthropic", "grok": "xai", "cursor": "cursor", "gemini": "google"}[h]
	if h == "pi" || h == "opencode" {
		expected, _, _ = strings.Cut(model, "/")
		if h == "pi" && !strings.Contains(model, "/") {
			expected = family
		}
	}
	if provider != expected {
		return fail(422, "provider route does not match model namespace")
	}
	if !modelRE.MatchString(model) || len(model) > 128 {
		return fail(422, "invalid model")
	}
	return nil
}

func (m *Module) editLine(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	if err := m.requirePermission(r, p, "models.manage"); err != nil {
		writeErr(w, err)
		return
	}
	h, model, err := linePath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in lineWrite
	if err = decodeJSON(w, r, &in); err == nil {
		err = validateLineWrite(in, h, model)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	out := struct {
		Profiles []Profile `json:"profiles"`
		Revision string    `json:"revision"`
	}{}
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := preferenceFence(ctx, tx, p); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "models.manage", authz.Scope{}); err != nil {
			return err
		}
		if err := catalogLock(ctx, tx); err != nil {
			return err
		}
		c, err := loadBoardCatalog(ctx, tx)
		if err != nil {
			return err
		}
		before := lineProfiles(c, h, model)
		if len(before) == 0 {
			return fail(404, "model line not found")
		}
		revision, err := registryRevision(ctx, tx)
		if err != nil {
			return err
		}
		if in.Revision != nil && *in.Revision != revision {
			return fail(409, "stale registry revision")
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		nextModel := in.Model
		if nextModel == "" {
			nextModel = model
		}
		if nextModel != model && len(lineProfiles(c, h, nextModel)) > 0 {
			return fail(409, "replacement model already has active profiles")
		}
		family := harnesslaunch.ModelFamily(h, nextModel)
		source := before[0].Source
		out.Profiles = []Profile{}
		for i, e := range in.Efforts {
			// Known efforts keep the shared scale; novel native names use their
			// explicitly ordered registration, bounded to the scale's endpoints.
			var scale *int
			if err := tx.QueryRow(ctx, `SELECT aeon_model_effort_level($1,$2)::integer`, family, e).Scan(&scale); err != nil {
				return err
			}
			if scale == nil {
				v := min(i, 5)
				scale = &v
			}
			sum := sha256.Sum256([]byte(h + "/" + nextModel + "/" + e))
			pin := profileWrite{Display: Display{DisplayName: strings.TrimSpace(in.DisplayName), ShortName: strings.TrimSpace(in.DisplayName)}, Slug: "edited-" + h + "-" + hex.EncodeToString(sum[:12]), Version: now.UTC().Format("20060102T150405.000000000"), Harness: h, Family: family, Model: nextModel, Effort: e, Tier: before[0].Tier, Note: strings.TrimSpace(in.Note), Source: source, RegisteredEffortLevel: scale}
			if err := validateProfile(pin); err != nil {
				return err
			}
			prof, err := insertActivatedProfile(ctx, tx, p, pin, before[0].Enabled, modelactivation.Person)
			if err != nil {
				return err
			}
			out.Profiles = append(out.Profiles, prof)
		}
		for _, old := range before {
			// Retirement rows reject UPDATE. Replace an existing future schedule
			// through the same delete/insert lifecycle used by Undo.
			if _, err := tx.Exec(ctx, `DELETE FROM model_profile_retirements WHERE profile_id=$1`, old.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by) VALUES($1,$2,'Edited in Settings › Models',$3)`, p.TenantID, old.ID, p.ID); err != nil {
				return err
			}
		}
		if err := transferLineReferences(ctx, tx, p, before, out.Profiles); err != nil {
			return err
		}
		out.Revision, err = registryRevision(ctx, tx)
		if err != nil {
			return err
		}
		return writeEvent(ctx, tx, p, "model.line_edited", before, out)
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func boardLineID(p Profile) string { f, l, _ := ProfileLine(p); return f + ":" + l }

// Caller holds the tenant and registry fences. All transfers precede events.
func transferLineReferences(ctx context.Context, tx pgx.Tx, p tenant.Principal, before, after []Profile) error {
	oldLine, newLine := boardLineID(before[0]), boardLineID(after[0])
	if oldLine != newLine {
		var collision bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_rules a JOIN model_rules b ON b.tenant_id=a.tenant_id AND b.scope=a.scope AND b.project_id IS NOT DISTINCT FROM a.project_id AND b.column_key=a.column_key WHERE a.line=$1 AND b.line=$2)`, oldLine, newLine).Scan(&collision); err != nil {
			return err
		}
		if collision {
			return fail(409, "replacement line has a conflicting rule")
		}

		if _, err := tx.Exec(ctx, `UPDATE model_pref_orders SET rank=CASE WHEN rank IS NULL THEN NULL ELSE ARRAY(SELECT v FROM unnest(array_replace(rank,$1,$2)) WITH ORDINALITY u(v,n) WHERE NOT v=ANY(array_replace(not_allowed,$1,$2)) GROUP BY v ORDER BY min(n)) END,not_allowed=ARRAY(SELECT v FROM unnest(array_replace(not_allowed,$1,$2)) WITH ORDINALITY u(v,n) GROUP BY v ORDER BY min(n)) WHERE $1=ANY(rank) OR $1=ANY(not_allowed)`, oldLine, newLine); err != nil {
			return err
		}
		// Preserve the order of the remaining entries while deduplicating aliases.
		if _, err := tx.Exec(ctx, `UPDATE model_rules SET line=$2 WHERE line=$1 AND NOT EXISTS(SELECT 1 FROM model_rules n WHERE n.tenant_id=model_rules.tenant_id AND n.scope=model_rules.scope AND n.project_id IS NOT DISTINCT FROM model_rules.project_id AND n.column_key=model_rules.column_key AND n.line=$2)`, oldLine, newLine); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE model_pref_profiles SET revision=revision+1 WHERE EXISTS(SELECT 1 FROM model_pref_orders o WHERE o.profile_id=model_pref_profiles.id AND ($1=ANY(o.rank) OR $2=ANY(o.rank) OR $1=ANY(o.not_allowed) OR $2=ANY(o.not_allowed)))`, oldLine, newLine); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE model_rule_revisions SET revision=revision+1 WHERE EXISTS(SELECT 1 FROM model_rules r WHERE r.scope=model_rule_revisions.scope AND coalesce(r.project_id::text,'')=model_rule_revisions.scope_key AND r.line IN ($1,$2))`, oldLine, newLine); err != nil {
		return err
	}
	for _, old := range before {
		replacement := nearestProfile(after, old.Effort, old.EffortLevel)
		for _, table := range []string{"model_role_routes", "model_security_role_routes"} {
			if _, err := tx.Exec(ctx, `UPDATE `+table+` SET profile_id=$2 WHERE profile_id=$1 AND NOT EXISTS(SELECT 1 FROM `+table+` n WHERE n.tenant_id=`+table+`.tenant_id AND n.role=`+table+`.role AND n.profile_id=$2)`, old.ID, replacement.ID); err != nil {
				return err
			}
		}
	}
	// Reconcile native names for all affected rows without touching situations,
	// not-allowed entries, templates, profile knobs or unrelated rules.
	rows, err := tx.Query(ctx, `SELECT profile_id::text,column_key,situation,rank,not_allowed,thinking,effort,effort_level FROM model_pref_orders WHERE rank[1]=$1 AND effort IS NOT NULL LIMIT 1537`, newLine)
	if err != nil {
		return err
	}
	orders := []modelprefs.BoardOrder{}
	for rows.Next() {
		var o modelprefs.BoardOrder
		if err := rows.Scan(&o.ProfileID, &o.Column, &o.Situation, &o.Rank, &o.Not, &o.Thinking, &o.Effort, &o.EffortLevel); err != nil {
			rows.Close()
			return err
		}
		orders = append(orders, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(orders) > 1536 {
		return modelprefs.ErrBoardBounds
	}
	for _, o := range orders {
		prof := nearestProfile(after, *o.Effort, o.EffortLevel)
		if _, err := tx.Exec(ctx, `UPDATE model_pref_orders SET effort=$4,effort_level=$5 WHERE profile_id=$1 AND column_key=$2 AND situation=$3`, o.ProfileID, o.Column, o.Situation, prof.Effort, prof.EffortLevel); err != nil {
			return err
		}
	}
	return nil
}

func nearestProfile(ps []Profile, effort string, level *int) Profile {
	best := ps[0]
	target := 3
	if level != nil {
		target = *level
	}
	distance := func(p Profile) int {
		if p.EffortLevel == nil {
			return 100
		}
		return abs(*p.EffortLevel - target)
	}
	for _, p := range ps {
		if p.Effort == effort {
			return p
		}
		if distance(p) < distance(best) || distance(p) == distance(best) && p.EffortLevel != nil && (best.EffortLevel == nil || *p.EffortLevel > *best.EffortLevel) {
			best = p
		}
	}
	return best
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Scheduled entries remain eligible until their date; the scheduler appends
// each normal retirement event exactly once, independent of refresh interval.
func applyScheduledRetirements(ctx context.Context, tx pgx.Tx, now time.Time) ([]events.Change, error) {
	rows, err := tx.Query(ctx, `SELECT profile_id::text,reason,retire_at FROM model_profile_retirements WHERE retire_at<=$1 AND applied_at IS NULL ORDER BY profile_id LIMIT 513 FOR NO KEY UPDATE`, now)
	if err != nil {
		return nil, err
	}
	changes := []events.Change{}
	ids := []string{}
	for rows.Next() {
		var id, reason string
		var at time.Time
		if err := rows.Scan(&id, &reason, &at); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		changes = append(changes, events.Change{Type: "model.profile_retired", After: map[string]any{"profile_id": id, "retired": true, "reason": reason, "retire_at": at}})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) > 512 {
		return nil, modelprefs.ErrBoardBounds
	}
	if _, err := tx.Exec(ctx, `WITH due AS (DELETE FROM model_profile_retirements WHERE profile_id=ANY($1::uuid[]) RETURNING *)
 INSERT INTO model_profile_retirements(tenant_id,profile_id,retired_at,reason,retired_by,retire_at,applied_at)
 SELECT tenant_id,profile_id,$2,reason,retired_by,retire_at,$2 FROM due`, ids, now); err != nil {
		return nil, err
	}
	return changes, nil
}
