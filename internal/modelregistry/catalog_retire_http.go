// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type outdatedVersion struct {
	Version string `json:"version"`
	Line    string `json:"line"`
	CatalogLifecycle
	Uses       []retireUse `json:"uses"`
	Incomplete bool        `json:"incomplete"`
}
type outdatedDocument struct {
	Versions   []outdatedVersion `json:"versions"`
	Revision   string            `json:"revision"`
	NextCursor string            `json:"next_cursor"`
	Incomplete bool              `json:"incomplete"`
}
type retireWrite struct {
	Versions []string `json:"versions"`
	Revision string   `json:"revision"`
}
type retireResult struct {
	Moved     []retireUse `json:"moved"`
	Removed   []string    `json:"removed"`
	UndoToken string      `json:"undo_token"`
}
type retireEventAfter struct {
	Snapshot  retireSnapshot `json:"snapshot"`
	TokenHash string         `json:"token_hash"`
	Removed   []string       `json:"removed"`
}

func outdated(model lifecycleModel) bool {
	state := model.Lifecycle.State
	if state == nil || (*state != "superseded" && *state != "deprecated" && *state != "retiring" && *state != "vanished") {
		return false
	}
	for _, p := range model.Profiles {
		if !p.Retired {
			return true
		}
	}
	return false
}
func retireRevision(ctx context.Context, tx pgx.Tx, models []lifecycleModel, s retireSnapshot) (string, error) {
	registry, err := registryRevision(ctx, tx)
	if err != nil {
		return "", err
	}
	return retireDigest(struct {
		Registry   string
		Models     []lifecycleModel
		References retireSnapshot
	}{registry, models, s}), nil
}
func retireAuthority(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Person {
		return fail(403, "person required")
	}
	if err := preferenceFence(ctx, tx, p); err != nil {
		return err
	}
	if err := catalogLock(ctx, tx); err != nil {
		return err
	}
	return authz.RequireTx(ctx, tx, p, "models.manage", authz.Scope{})
}
func (m *Module) outdatedModels(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			writeErr(w, fail(400, "outdated limit must be from 1 to 50"))
			return
		}
		limit = n
	}
	after := r.URL.Query().Get("cursor")
	if len(after) > 200 {
		writeErr(w, fail(400, "invalid outdated cursor"))
		return
	}
	out := outdatedDocument{Versions: []outdatedVersion{}}
	usageBudget := 4096
	err := m.readSnapshot(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		current, err := currentModelReader(r, tx, p)
		if err != nil {
			return err
		}
		now, err := dbNow(r.Context(), tx)
		if err != nil {
			return err
		}
		models, err := loadLifecycleCatalog(r.Context(), tx, now)
		if err != nil {
			return err
		}
		snapshot, err := loadRetireSnapshot(r.Context(), tx)
		if err != nil {
			return err
		}
		out.Revision, err = retireRevision(r.Context(), tx, models, snapshot)
		if err != nil {
			return err
		}
		for _, model := range models {
			if model.Identity <= after || !outdated(model) {
				continue
			}
			if len(out.Versions) == limit {
				out.NextCursor = out.Versions[len(out.Versions)-1].Version
				break
			}
			allUses := retirementUses(model, snapshot, models)
			usageBudget -= len(allUses)
			if len(allUses) > 2048 || usageBudget < 0 {
				return fail(413, "retirement usage limit")
			}
			uses, incomplete, err := visibleRetireUses(r.Context(), tx, current, allUses)
			if err != nil {
				return err
			}
			out.Incomplete = out.Incomplete || incomplete
			out.Versions = append(out.Versions, outdatedVersion{Version: model.Identity, Line: model.Provider + ":" + model.Line, CatalogLifecycle: model.Lifecycle, Uses: uses, Incomplete: incomplete})
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func validateRetireWrite(in retireWrite) error {
	if len(in.Versions) < 1 || len(in.Versions) > 50 || len(in.Revision) != 64 {
		return fail(400, "versions and preview revision required")
	}
	if _, err := hex.DecodeString(in.Revision); err != nil {
		return fail(400, "invalid preview revision")
	}
	seen := map[string]bool{}
	for _, id := range in.Versions {
		provider, model, ok := strings.Cut(id, ":")
		if !ok || len(provider) > 64 || len(model) > 128 || !modelRE.MatchString(provider) || !modelRE.MatchString(model) || seen[id] {
			return fail(400, "invalid or duplicate version")
		}
		seen[id] = true
	}
	return nil
}
func (m *Module) retireOutdated(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeErr(w, fail(403, "person required"))
		return
	}
	var in retireWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := validateRetireWrite(in); err != nil {
		writeErr(w, err)
		return
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		writeErr(w, err)
		return
	}
	secret := hex.EncodeToString(token)
	out := retireResult{Moved: []retireUse{}, Removed: []string{}}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := retireAuthority(ctx, tx, p); err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		models, err := loadLifecycleCatalog(ctx, tx, now)
		if err != nil {
			return err
		}
		before, err := loadRetireSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		revision, err := retireRevision(ctx, tx, models, before)
		if err != nil {
			return err
		}
		if revision != in.Revision {
			return fail(409, "stale outdated preview")
		}
		selected := map[string]bool{}
		byID := map[string]lifecycleModel{}
		for _, id := range in.Versions {
			selected[id] = true
		}
		for _, model := range models {
			byID[model.Identity] = model
		}
		// Resolve and authorize the entire batch before its first write.
		type transfer struct {
			model         lifecycleModel
			before, after []Profile
			uses          []retireUse
		}
		transfers := []transfer{}
		for _, id := range in.Versions {
			model, found := byID[id]
			if !found || !outdated(model) {
				return fail(409, "version is no longer outdated")
			}
			uses := retirementUses(model, before, models)
			if len(uses) > 2048 {
				return fail(413, "retirement usage limit")
			}
			totalUses := len(uses)
			for _, tr := range transfers {
				totalUses += len(tr.uses)
			}
			if totalUses > 4096 {
				return fail(413, "retirement usage limit")
			}
			if len(uses) > 0 {
				if err := authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{}); err != nil {
					return err
				}
				for _, use := range uses {
					if use.project != "" {
						if err := authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{ProjectID: use.project}); err != nil {
							return err
						}
						if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: use.project}); err != nil {
							return err
						}
					}
				}
			}
			tr := transfer{model: model, uses: uses}
			for _, old := range model.Profiles {
				if !old.Retired {
					tr.before = append(tr.before, old)
				}
			}
			if model.Lifecycle.Successor != nil {
				if selected[*model.Lifecycle.Successor] {
					return fail(409, "successor is also selected for retirement")
				}
				next := byID[*model.Lifecycle.Successor]
				for _, candidate := range next.Profiles {
					if candidate.Enabled && !candidate.Retired {
						tr.after = append(tr.after, candidate)
					}
				}
			}
			if len(uses) > 0 && len(tr.after) == 0 {
				return fail(409, "used version has no registered successor")
			}
			// An exact dispatch reference never switches harness or family implicitly.
			for _, old := range tr.before {
				if profileReferenced(before, old.ID) {
					if len(sameRouteProfiles(tr.after, old)) == 0 {
						return fail(409, "successor has no matching harness route")
					}
				}
			}
			transfers = append(transfers, tr)
		}
		for _, tr := range transfers {
			if len(tr.after) > 0 && len(tr.uses) > 0 {
				if err := transferLineReferences(ctx, tx, p, tr.before, tr.after, len(retirementLines(tr.model, models)) > 0); err != nil {
					return err
				}
				for _, old := range tr.before {
					candidates := sameRouteProfiles(tr.after, old)
					if len(candidates) == 0 {
						continue
					}
					replacement := nearestProfile(candidates, old.Effort, old.EffortLevel)
					// Legacy immutable pin cells remain supported during the board transition.
					if _, err := tx.Exec(ctx, `UPDATE model_pref_scopes s SET revision=revision+1 WHERE EXISTS(SELECT 1 FROM model_pref_cells c WHERE c.scope_id=s.id AND c.profile_id=$1)`, old.ID); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `UPDATE model_pref_cells SET profile_id=$2 WHERE profile_id=$1`, old.ID, replacement.ID); err != nil {
						return err
					}
				}
			}
			for _, old := range tr.before {
				if _, err := tx.Exec(ctx, `DELETE FROM model_profile_retirements WHERE profile_id=$1`, old.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by) VALUES($1,$2,'Retired outdated model',$3)`, p.TenantID, old.ID, p.ID); err != nil {
					return err
				}
			}
			out.Moved = append(out.Moved, tr.uses...)
			out.Removed = append(out.Removed, tr.model.Identity)
		}
		after, err := loadRetireSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		changedBefore, changedAfter := changedRetireSnapshots(before, after)
		event, err := events.Append(ctx, tx, p, events.Change{Type: "model.outdated_retired", Before: changedBefore, After: retireEventAfter{Snapshot: changedAfter, TokenHash: retireDigest(secret), Removed: out.Removed}})
		if err != nil {
			return err
		}
		out.UndoToken = strconv.FormatInt(event.ID, 10) + "." + secret
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func sameRouteProfiles(profiles []Profile, old Profile) []Profile {
	out := []Profile{}
	for _, p := range profiles {
		if p.Harness == old.Harness && p.Family == old.Family {
			out = append(out, p)
		}
	}
	return out
}
func profileReferenced(s retireSnapshot, id string) bool {
	for _, table := range []string{"model_role_routes", "model_security_role_routes", "model_pref_cells"} {
		for _, raw := range s[table] {
			if rowString(rowFields(raw), "profile_id") == id {
				return true
			}
		}
	}
	return false
}

func (m *Module) undoOutdated(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeErr(w, fail(403, "person required"))
		return
	}
	var in struct {
		UndoToken string `json:"undo_token"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	idRaw, secret, ok := strings.Cut(in.UndoToken, ".")
	id, err := strconv.ParseInt(idRaw, 10, 64)
	if !ok || err != nil || id < 1 || len(secret) != 64 || len(idRaw) > 19 {
		writeErr(w, fail(400, "invalid undo token"))
		return
	}
	if _, err := hex.DecodeString(secret); err != nil {
		writeErr(w, fail(400, "invalid undo token"))
		return
	}
	var eventID int64
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := retireAuthority(ctx, tx, p); err != nil {
			return err
		}
		// This endpoint is an ordinary compensating event and retains Undo authority.
		if err := authz.RequireTx(ctx, tx, p, "events.undo", authz.Scope{}); err != nil {
			return err
		}
		var actor string
		var beforeRaw, afterRaw json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT actor_principal_id::text,before,after FROM events WHERE id=$1 AND type='model.outdated_retired' AND octet_length(before::text)<=4194304 AND octet_length(after::text)<=4194304`, id).Scan(&actor, &beforeRaw, &afterRaw); err != nil {
			if err == pgx.ErrNoRows {
				return fail(404, "retirement not found")
			}
			return err
		}
		if actor != p.ID {
			if err := authz.RequireTx(ctx, tx, p, "events.undo_other", authz.Scope{}); err != nil {
				return err
			}
		}
		var before retireSnapshot
		var after retireEventAfter
		if json.Unmarshal(beforeRaw, &before) != nil || json.Unmarshal(afterRaw, &after) != nil || after.TokenHash != retireDigest(secret) {
			return fail(409, "invalid retirement undo")
		}
		var undone bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE undo_of=$1)`, id).Scan(&undone); err != nil {
			return err
		}
		if undone {
			return fail(409, "retirement already undone")
		}
		for _, table := range []string{"model_pref_orders", "model_rules", "model_pref_cells"} {
			if len(before[table])+len(after.Snapshot[table]) > 0 {
				if err := authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{}); err != nil {
					return err
				}
			}
		}
		for _, raw := range append(append([]json.RawMessage{}, before["model_rules"]...), before["model_pref_scopes"]...) {
			project := rowString(rowFields(raw), "project_id")
			if project != "" {
				if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
					return err
				}
				if err := authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{ProjectID: project}); err != nil {
					return err
				}
			}
		}
		current, err := loadRetireSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		if err := undoRetireSnapshots(ctx, tx, before, after.Snapshot, current); err != nil {
			return err
		}
		restored, err := loadRetireSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		actualBefore, actualAfter := changedRetireSnapshots(current, restored)
		event, err := events.Append(ctx, tx, p, events.Change{Type: "model.outdated_restored", Before: actualBefore, After: actualAfter, UndoOf: &id})
		eventID = event.ID
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"event_id": eventID, "restored": true})
}
