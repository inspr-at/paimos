// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type boardMoved struct {
	Column string   `json:"column"`
	Before []string `json:"before"`
	After  []string `json:"after"`
}
type boardWriteResult struct {
	PersonID       *string                 `json:"person_id"`
	Revision       int64                   `json:"revision"`
	Profile        modelprefs.BoardProfile `json:"profile"`
	DryRun         bool                    `json:"dry_run"`
	Moved          []boardMoved            `json:"moved"`
	RunningOutside []string                `json:"running_outside"`
}
type boardOrderWrite struct {
	Rank     []string `json:"rank"`
	Not      []string `json:"not"`
	Revision *int64   `json:"revision"`
	Thinking *string  `json:"thinking"`
}

func boardFor(r *http.Request) (string, error) {
	target := r.URL.Query().Get("for")
	if target == "" || target == "me" {
		return "person", nil
	}
	if target == "default" {
		return "default", nil
	}
	return "", prefFail(400, "invalid_preference_target")
}

// The tenant fence serializes grants, identity links and every board revision.
func boardWriteAuthority(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, level, project string) (*string, error) {
	if err := preferenceFence(ctx, tx, p); err != nil {
		return nil, err
	}
	if err := workorders.RefreshPrincipal(ctx, tx, p); err != nil {
		return nil, err
	}
	person, err := currentPreferencePerson(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	if err := authorizePreference(ctx, tx, p, level, project, person); err != nil {
		return nil, err
	}
	expected, err := expectedPreferencePerson(r, level)
	if err != nil {
		return nil, err
	}
	if expected != "" && (person == nil || *person != expected) {
		return nil, prefFail(409, "preference_person_changed")
	}
	return person, nil
}
func targetBoardProfile(s modelprefs.BoardState, level string) modelprefs.BoardProfile {
	if level == "person" && s.Person != nil {
		return *s.Person
	}
	return s.Workspace
}
func persistBoardProfile(ctx context.Context, tx pgx.Tx, p tenant.Principal, profile modelprefs.BoardProfile) (modelprefs.BoardProfile, error) {
	profile.Revision++
	if profile.ID == "" {
		err := tx.QueryRow(ctx, `INSERT INTO model_pref_profiles(tenant_id,scope,person_id,template,thinking,usage,residency,hidden_kinds,dismissed_lines,revision,set_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id::text`, p.TenantID, profile.Scope, profile.PersonID, profile.Template, profile.Thinking, profile.Usage, profile.Residency, profile.HiddenKinds, profile.DismissedLines, profile.Revision, p.ID).Scan(&profile.ID)
		return profile, err
	}
	_, err := tx.Exec(ctx, `UPDATE model_pref_profiles SET template=$2,thinking=$3,usage=$4,residency=$5,hidden_kinds=$6,dismissed_lines=$7,revision=$8,set_by=$9,set_at=now() WHERE id=$1`, profile.ID, profile.Template, profile.Thinking, profile.Usage, profile.Residency, profile.HiddenKinds, profile.DismissedLines, profile.Revision, p.ID)
	return profile, err
}
func checkBoardRevision(profile modelprefs.BoardProfile, revision *int64) error {
	if revision == nil || *revision < 0 {
		return prefFail(400, "revision_required")
	}
	if profile.Revision != *revision {
		return prefFail(409, "stale_revision")
	}
	return nil
}
func validBoardUsage(s string) bool { return s == "careful" || s == "balanced" || s == "maxout" }
func validThinking(s *string) bool {
	return s == nil || *s == "lean" || *s == "standard" || *s == "deep" || *s == "max"
}
func validateBoardLines(c boardCatalog, rank, not []string) error {
	if len(rank)+len(not) > 512 {
		return prefFail(413, "too_many_lines")
	}
	seen := map[string]bool{}
	for _, ids := range [][]string{rank, not} {
		for _, id := range ids {
			if len(id) > 128 || seen[id] {
				return prefFail(422, "invalid_line_order")
			}
			seen[id] = true
			if _, ok := c.profiles[id]; !ok {
				return prefFail(422, "unknown_line")
			}
		}
	}
	return nil
}
func putBoardOrder(ctx context.Context, tx pgx.Tx, p tenant.Principal, profile modelprefs.BoardProfile, o modelprefs.BoardOrder) error {
	_, err := tx.Exec(ctx, `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,not_allowed,thinking,set_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(tenant_id,profile_id,column_key,situation) DO UPDATE SET rank=excluded.rank,not_allowed=excluded.not_allowed,thinking=excluded.thinking,set_by=excluded.set_by,set_at=now()`, p.TenantID, profile.ID, o.Column, o.Situation, o.Rank, o.Not, o.Thinking, p.ID)
	return err
}
func boardResult(profile modelprefs.BoardProfile, person *string) boardWriteResult {
	return boardWriteResult{PersonID: person, Profile: profile, Revision: profile.Revision, Moved: []boardMoved{}, RunningOutside: []string{}}
}
func (m *Module) writeBoardOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	level, err := boardFor(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	column, situation := r.PathValue("column"), r.PathValue("situation")
	if len(column) > 64 || !validBoardSituation(situation) {
		writePreferenceError(w, prefFail(400, "invalid_board_order"))
		return
	}
	var in boardOrderWrite
	thinking := strings.HasSuffix(r.URL.Path, "/thinking")
	if r.Method == http.MethodDelete {
		rev, err := requestedRevision(r)
		if err != nil {
			writePreferenceError(w, err)
			return
		}
		in.Revision = &rev
	} else {
		if err := workBody(w, r); err != nil {
			writePreferenceError(w, err)
			return
		}
		// Distinct DTOs reject irrelevant fields before any mutation.
		if thinking {
			var body struct {
				Thinking json.RawMessage `json:"thinking"`
				Revision *int64          `json:"revision"`
			}
			err = decodeJSON(w, r, &body)
			in.Revision = body.Revision
			if err == nil {
				if len(body.Thinking) == 0 {
					err = prefFail(422, "thinking_required")
				} else if json.Unmarshal(body.Thinking, &in.Thinking) != nil {
					err = prefFail(422, "invalid_thinking")
				}
			}
		} else {
			var body struct {
				Rank     []string `json:"rank"`
				Not      []string `json:"not"`
				Revision *int64   `json:"revision"`
			}
			err = decodeJSON(w, r, &body)
			in.Rank, in.Not, in.Revision = body.Rank, body.Not, body.Revision
			if err == nil && (in.Rank == nil || in.Not == nil) {
				err = prefFail(422, "rank_and_not_required")
			}
		}
		if err != nil {
			writePreferenceError(w, err)
			return
		}
	}
	if !validThinking(in.Thinking) {
		writePreferenceError(w, prefFail(422, "invalid_thinking"))
		return
	}
	var out boardWriteResult
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		person, err := boardWriteAuthority(ctx, tx, r, p, level, "")
		if err != nil {
			return err
		}
		s, err := modelprefs.LoadBoard(ctx, tx, person, "")
		if err != nil {
			return err
		}
		if !validBoardColumn(s, column) {
			return prefFail(422, "unknown_column")
		}
		profile := targetBoardProfile(s, level)
		if err := checkBoardRevision(profile, in.Revision); err != nil {
			return err
		}
		c, err := loadBoardCatalog(ctx, tx)
		if err != nil {
			return err
		}
		if !thinking && r.Method != http.MethodDelete {
			if err := validateBoardLines(c, in.Rank, in.Not); err != nil {
				return err
			}
		}
		var before *modelprefs.BoardOrder
		for _, o := range s.Orders {
			if o.ProfileID == profile.ID && o.Column == column && o.Situation == situation {
				copy := o
				before = &copy
				break
			}
		}
		profile, err = persistBoardProfile(ctx, tx, p, profile)
		if err != nil {
			return err
		}
		var after *modelprefs.BoardOrder
		if r.Method == http.MethodDelete {
			if _, err := tx.Exec(ctx, `DELETE FROM model_pref_orders WHERE profile_id=$1 AND column_key=$2 AND situation=$3`, profile.ID, column, situation); err != nil {
				return err
			}
		} else {
			o := modelprefs.BoardOrder{ProfileID: profile.ID, Column: column, Situation: situation, Rank: in.Rank, Not: in.Not, Thinking: in.Thinking}
			if thinking {
				o.Not = []string{}
				if before != nil {
					o = *before
					o.Thinking = in.Thinking
				}
			}
			if !thinking && before != nil {
				o.Thinking = before.Thinking
			}
			if err := putBoardOrder(ctx, tx, p, profile, o); err != nil {
				return err
			}
			after = &o
		}
		out = boardResult(profile, person)
		_, err = events.Append(ctx, tx, p, events.Change{Type: "model.preferences_changed", Before: map[string]any{"profile": targetBoardProfile(s, level), "order": before}, After: map[string]any{"profile": profile, "order": after, "column": column, "situation": situation}})
		return err
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func profilePatch(profile modelprefs.BoardProfile, raw map[string]json.RawMessage) (modelprefs.BoardProfile, bool, error) {
	// Preserve the comparison snapshot: json.Unmarshal reuses existing pointers
	// and array capacity when decoding into a shallow struct copy.
	clone := func(v *string) *string {
		if v == nil {
			return nil
		}
		value := *v
		return &value
	}
	profile.Template, profile.Thinking, profile.Usage, profile.Residency = clone(profile.Template), clone(profile.Thinking), clone(profile.Usage), clone(profile.Residency)
	profile.HiddenKinds, profile.DismissedLines = slices.Clone(profile.HiddenKinds), slices.Clone(profile.DismissedLines)
	replace := false
	for key, value := range raw {
		var err error
		switch key {
		case "revision":
			continue
		case "replace_own":
			err = json.Unmarshal(value, &replace)
		case "template":
			err = json.Unmarshal(value, &profile.Template)
		case "thinking":
			err = json.Unmarshal(value, &profile.Thinking)
		case "usage":
			err = json.Unmarshal(value, &profile.Usage)
		case "residency":
			err = json.Unmarshal(value, &profile.Residency)
		case "hidden_kinds":
			err = json.Unmarshal(value, &profile.HiddenKinds)
		case "dismissed_lines":
			err = json.Unmarshal(value, &profile.DismissedLines)
		default:
			return profile, false, prefFail(400, "unknown_profile_field")
		}
		if err != nil {
			return profile, false, prefFail(400, "invalid_profile")
		}
	}
	if profile.Usage != nil && !validBoardUsage(*profile.Usage) || profile.Template != nil && *profile.Template != "best" && *profile.Template != "balanced" && *profile.Template != "save" || !validThinking(profile.Thinking) || profile.Residency != nil && *profile.Residency != "any" && *profile.Residency != "eu" && *profile.Residency != "local" {
		return profile, false, prefFail(422, "invalid_profile")
	}
	if profile.HiddenKinds == nil || profile.DismissedLines == nil {
		return profile, false, prefFail(422, "invalid_profile")
	}
	if len(profile.HiddenKinds) > 256 || len(profile.DismissedLines) > 512 {
		return profile, false, prefFail(413, "too_many_lines")
	}
	return profile, replace, nil
}
func (m *Module) writeBoardProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	level, err := boardFor(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	if err := workBody(w, r); err != nil {
		writePreferenceError(w, err)
		return
	}
	var raw map[string]json.RawMessage
	if err := decodeJSON(w, r, &raw); err != nil {
		writePreferenceError(w, err)
		return
	}
	var revision *int64
	if err := json.Unmarshal(raw["revision"], &revision); err != nil {
		writePreferenceError(w, prefFail(400, "revision_required"))
		return
	}
	dry := false
	if r.URL.Query().Has("dry_run") {
		dry, err = strconv.ParseBool(r.URL.Query().Get("dry_run"))
		if err != nil {
			writePreferenceError(w, prefFail(400, "invalid_dry_run"))
			return
		}
	}
	var out boardWriteResult
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		person, err := boardWriteAuthority(ctx, tx, r, p, level, "")
		if err != nil {
			return err
		}
		s, err := modelprefs.LoadBoard(ctx, tx, person, "")
		if err != nil {
			return err
		}
		if level == "default" {
			s.Person = nil
		}
		before := targetBoardProfile(s, level)
		if err := checkBoardRevision(before, revision); err != nil {
			return err
		}
		profile, replace, err := profilePatch(before, raw)
		if err != nil {
			return err
		}
		if level == "person" && profile.Residency != nil {
			base, err := boardResidency(ctx, tx, modelprefs.BoardState{Workspace: s.Workspace}, nil, "", "")
			if err != nil {
				return err
			}
			if modelprefs.Strictest(base, *profile.Residency) != *profile.Residency {
				return prefFail(422, "looser_than_workspace")
			}
		}
		seen := map[string]bool{}
		for _, slug := range profile.HiddenKinds {
			if len(slug) > 48 || seen[slug] || slug == "other" || slug == "concept" || strings.HasPrefix(slug, "review:") || !validBoardColumn(s, slug) {
				return prefFail(422, "invalid_hidden_kind")
			}
			seen[slug] = true
		}
		c, err := loadBoardCatalog(ctx, tx)
		if err != nil {
			return err
		}
		if err := validateBoardLines(c, profile.DismissedLines, nil); err != nil {
			return err
		}
		s.Lines = c.lines
		after := s
		if level == "person" {
			after.Person = &profile
		} else {
			after.Workspace = profile
		}
		if replace {
			after.Orders = slices.DeleteFunc(slices.Clone(after.Orders), func(o modelprefs.BoardOrder) bool { return o.ProfileID == before.ID })
		}
		out = boardResult(profile, person)
		out.DryRun = dry
		for _, column := range boardColumns(s) {
			old := modelprefs.ResolveBoard(s, modelprefs.BoardQuery{Column: column.Slug, Situation: "first"}, nil)
			next := modelprefs.ResolveBoard(after, modelprefs.BoardQuery{Column: column.Slug, Situation: "first"}, nil)
			if !reflect.DeepEqual(old.Rank, next.Rank) {
				out.Moved = append(out.Moved, boardMoved{column.Slug, old.Rank, next.Rank})
			}
		}
		if dry {
			return nil
		}
		scope := modelprefs.Scope{Level: level, PersonID: profile.PersonID}
		var prior map[string]string
		if !reflect.DeepEqual(before.Residency, profile.Residency) {
			prior, err = modelprefs.SnapshotRunRequirements(ctx, tx, scope)
			if err != nil {
				return err
			}
		}
		profile, err = persistBoardProfile(ctx, tx, p, profile)
		if err != nil {
			return err
		}
		// Providers remain the existing scope residency store. Keep its mutable
		// value synchronized so a migrated personal limit can also be loosened.
		if _, supplied := raw["residency"]; supplied {
			chain, err := modelprefs.LoadChain(ctx, tx, profile.PersonID, "")
			if err != nil {
				return err
			}
			index := 0
			if level == "person" {
				index = 1
			}
			providerScope := chain[index]
			providerScope.Level, providerScope.PersonID = level, profile.PersonID
			providerScope.Residency = profile.Residency
			if _, err := modelprefs.SaveScopeOnly(ctx, tx, p, providerScope); err != nil {
				return err
			}
		}
		if replace && before.ID != "" {
			if _, err := tx.Exec(ctx, `DELETE FROM model_pref_orders WHERE profile_id=$1`, before.ID); err != nil {
				return err
			}
		}
		restamped := []events.Change{}
		if prior != nil {
			runs, changes, err := modelprefs.RestampAfterDeferred(ctx, tx, scope, prior)
			if err != nil {
				return err
			}
			restamped = changes
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
		out.Profile, out.Revision = profile, profile.Revision
		_, err = events.Append(ctx, tx, p, events.Change{Type: "model.preferences_changed", Before: map[string]any{"profile": before, "orders": s.Orders}, After: map[string]any{"profile": profile, "orders": after.Orders, "moved": out.Moved, "restamped": restamped, "running_outside": out.RunningOutside}})
		return err
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) dismissBoardLine(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	level, err := boardFor(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	var in struct {
		Revision *int64 `json:"revision"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writePreferenceError(w, err)
		return
	}
	line := r.PathValue("line")
	if len(line) > 128 {
		writePreferenceError(w, prefFail(422, "unknown_line"))
		return
	}
	var out boardWriteResult
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		person, err := boardWriteAuthority(ctx, tx, r, p, level, "")
		if err != nil {
			return err
		}
		s, err := modelprefs.LoadBoard(ctx, tx, person, "")
		if err != nil {
			return err
		}
		profile := targetBoardProfile(s, level)
		if err := checkBoardRevision(profile, in.Revision); err != nil {
			return err
		}
		c, err := loadBoardCatalog(ctx, tx)
		if err != nil {
			return err
		}
		if _, ok := c.profiles[line]; !ok {
			return prefFail(422, "unknown_line")
		}
		before := profile
		if !slices.Contains(profile.DismissedLines, line) {
			profile.DismissedLines = append(slices.Clone(profile.DismissedLines), line)
		}
		if len(profile.DismissedLines) > 512 {
			return prefFail(413, "too_many_lines")
		}
		profile, err = persistBoardProfile(ctx, tx, p, profile)
		if err != nil {
			return err
		}
		out = boardResult(profile, person)
		_, err = events.Append(ctx, tx, p, events.Change{Type: "model.preferences_changed", Before: before, After: profile})
		return err
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
