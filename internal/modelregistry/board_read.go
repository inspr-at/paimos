// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type boardCatalog struct {
	profiles   map[string][]Profile
	introduced map[string]time.Time
	lines      []modelprefs.BoardLine
}

func loadBoardCatalog(ctx context.Context, tx pgx.Tx) (boardCatalog, error) {
	out := boardCatalog{profiles: map[string][]Profile{}, introduced: map[string]time.Time{}, lines: []modelprefs.BoardLine{}}
	var large bool
	if err := tx.QueryRow(ctx, `SELECT count(*)>512 FROM (SELECT 1 FROM model_profiles LIMIT 513) p`).Scan(&large); err != nil {
		return out, err
	}
	if large {
		return out, modelprefs.ErrBoardBounds
	}
	profiles, err := listProfiles(ctx, tx)
	if err != nil {
		return out, err
	}
	for _, p := range profiles {
		family, line, _ := ProfileLine(p)
		id := family + ":" + line
		out.profiles[id] = append(out.profiles[id], p)
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text,coalesce(d.introduced_at,p.created_at) FROM model_profiles p JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id LIMIT 513`)
	if err != nil {
		return out, err
	}
	stamp := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return out, err
		}
		stamp[id] = at
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	keys := []string{}
	for id := range out.profiles {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		ps := out.profiles[id]
		l := modelprefs.BoardLine{ID: id, Family: ps[0].Family, Tools: true}
		if l.Family == "xai" {
			l.Tools = false
		}
		for _, p := range ps {
			at := stamp[p.ID]
			if out.introduced[id].IsZero() || at.Before(out.introduced[id]) {
				out.introduced[id] = at
			}
			if p.Effort == "xhigh" && (p.Tier == "strong" || p.Tier == "frontier") {
				l.Review = true
			}
		}
		out.lines = append(out.lines, l)
	}
	return out, nil
}
func (c boardCatalog) candidates(id string, level int, review bool) []Profile {
	return c.effortCandidates(id, "", level, review)
}
func (c boardCatalog) effortCandidates(id, effort string, level int, review bool) []Profile {
	ps := c.profiles[id]
	latest := ""
	for _, p := range ps {
		if !p.Enabled {
			continue
		}
		_, _, v := ProfileLine(p)
		if CompareModelVersions(v, latest) > 0 {
			latest = v
		}
	}
	activeVersion := false
	for _, p := range ps {
		_, _, v := ProfileLine(p)
		activeVersion = activeVersion || v == latest && p.Enabled && !p.Retired
	}
	out := []Profile{}
	levels := []int{}
	for _, p := range ps {
		_, _, v := ProfileLine(p)
		if v != latest || activeVersion && (p.Retired || !p.Enabled) {
			continue
		}
		if review && (p.Effort != "xhigh" || p.Tier != "strong" && p.Tier != "frontier") {
			continue
		}
		out = append(out, p)
		if p.EffortLevel != nil {
			levels = append(levels, *p.EffortLevel)
		}
	}
	exact := false
	for _, p := range out {
		if effort != "" && p.Effort == effort {
			exact = true
		}
	}
	nearest := modelprefs.NearestEffort(level, levels)
	out = slices.DeleteFunc(out, func(p Profile) bool {
		if exact {
			return p.Effort != effort
		}
		return !review && nearest >= 0 && (p.EffortLevel == nil || *p.EffortLevel != nearest)
	})
	harnessRank := map[string]int{"codex": 0, "claude": 1, "grok": 2, "pi": 3, "cursor": 4, "gemini": 5, "opencode": 6}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if harnessRank[a.Harness] != harnessRank[b.Harness] {
			return harnessRank[a.Harness] < harnessRank[b.Harness]
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return out
}

type boardCard struct {
	Line         string                `json:"line"`
	Version      string                `json:"version"`
	Harness      string                `json:"harness"`
	Effort       string                `json:"effort"`
	IntroducedAt time.Time             `json:"introduced_at"`
	Lock         *modelprefs.BoardLock `json:"lock,omitempty"`
}
type boardColumn struct {
	Column       string `json:"column"`
	Label        string `json:"label"`
	Short        string `json:"short"`
	Sentence     string `json:"sentence"`
	Fixed        bool   `json:"fixed"`
	Hidden       bool   `json:"hidden"`
	Source       string `json:"source"`
	FollowsFirst bool   `json:"follows_first"`
	Thinking     struct {
		Word       string  `json:"word"`
		Source     string  `json:"source"`
		FromColumn bool    `json:"from_column"`
		Own        *string `json:"own"`
	} `json:"thinking"`
	List   []boardCard           `json:"list"`
	Top    []boardCard           `json:"top"`
	Free   []boardCard           `json:"free"`
	Bottom []boardCard           `json:"bottom"`
	Not    []boardCard           `json:"not"`
	Cant   []modelprefs.HeldLine `json:"cant"`
}
type boardDocument struct {
	PersonID  *string                 `json:"person_id"`
	Layer     string                  `json:"layer"`
	Situation string                  `json:"situation"`
	Revision  int64                   `json:"revision"`
	Profile   modelprefs.BoardProfile `json:"profile"`
	Columns   []boardColumn           `json:"columns"`
	Tray      []boardCard             `json:"tray"`
	NeedsYou  []string                `json:"needs_you"`
	Residency struct {
		Own       *string `json:"own"`
		Effective string  `json:"effective"`
	} `json:"residency"`
}

func boardColumns(s modelprefs.BoardState) []modelprefs.Kind {
	out := []modelprefs.Kind{}
	other := modelprefs.Kind{Slug: "other", Label: "Everything else", Hint: "Any ticket no other kind describes, and every kind without its own column."}
	for _, k := range s.Kinds {
		if k.Slug == "other" {
			other = k
		} else if k.Slug != "review" {
			out = append(out, k)
		}
	}
	out = append(out, other)
	for _, a := range []struct{ family, label string }{{"openai", "Codex"}, {"anthropic", "Claude"}, {"xai", "Grok"}} {
		out = append(out, modelprefs.Kind{Slug: "review:" + a.family, Label: "Review of " + a.label, Hint: "Checks what a " + a.label + " model wrote, before it merges."})
	}
	return append(out, modelprefs.Kind{Slug: "concept", Label: "Concepts", Hint: "Product or architecture concepts and ADRs, written only on request. Not UI designs."})
}
func boardResidency(ctx context.Context, tx pgx.Tx, s modelprefs.BoardState, person *string, project, ticket string) (string, error) {
	chain, err := modelprefs.LoadChain(ctx, tx, person, project)
	if err != nil {
		return "", err
	}
	effective := modelprefs.Strictest(modelprefs.ResolveResidency(chain).Value, ticket)
	if s.Workspace.Residency != nil {
		effective = modelprefs.Strictest(effective, *s.Workspace.Residency)
	}
	if s.Person != nil && s.Person.Residency != nil {
		effective = modelprefs.Strictest(effective, *s.Person.Residency)
	}
	return effective, nil
}

// Residency is a hard route restriction, independent of temporary capacity.
func (c boardCatalog) residencyCounts(ctx context.Context, tx pgx.Tx, project, requirement string) (map[string]int, error) {
	profiles := map[string]string{}
	for _, ps := range c.profiles {
		for _, p := range ps {
			profiles[p.ID] = p.Harness
		}
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	return agentaccounts.ResidencyProfileRouteCounts(ctx, tx, profiles, project, requirement, now)
}
func (c boardCatalog) residencyLines(lines []modelprefs.BoardLine, counts map[string]int, d modelprefs.BoardDecision, requirement string) []modelprefs.BoardLine {
	out := slices.Clone(lines)
	if modelprefs.Strictness(requirement) == 0 {
		return out
	}
	for i := range out {
		allowed := false
		for _, p := range c.candidates(out[i].ID, d.EffortLevel, strings.HasPrefix(d.Column, "review:")) {
			if counts[p.ID] > 0 {
				allowed = true
				break
			}
		}
		if !allowed {
			out[i].Residency = &modelprefs.BoardLock{Kind: "residency", Value: "not", Why: "No qualifying route for the effective " + requirement + " residency", Scope: "effective"}
		}
	}
	return out
}

func (c boardCatalog) card(id string, d modelprefs.BoardDecision) boardCard {
	level := d.EffortLevel
	if level == 0 {
		level = modelprefs.ThinkingLevel(d.Thinking)
	}
	ps := c.effortCandidates(id, d.Effort, level, strings.HasPrefix(d.Column, "review:"))
	if len(ps) == 0 {
		ps = c.candidates(id, 4, false)
	}
	card := boardCard{Line: id, IntroducedAt: c.introduced[id], Lock: d.Locks[id]}
	if len(ps) > 0 {
		p := ps[0]
		_, _, card.Version = ProfileLine(p)
		if p.ModelVersion != "" {
			card.Version = p.ModelVersion
		}
		card.Harness = p.Harness
		card.Effort = p.Effort
	}
	return card
}
func boardDocumentFor(ctx context.Context, tx pgx.Tx, s modelprefs.BoardState, c boardCatalog, person *string, project, layer, situation string) (boardDocument, error) {
	out := boardDocument{PersonID: person, Layer: layer, Situation: situation, Columns: []boardColumn{}, Tray: []boardCard{}, NeedsYou: []string{}, Profile: s.Workspace}
	if layer == "mine" && s.Person != nil {
		out.Profile = *s.Person
	}
	residencyPerson := person
	if layer != "mine" {
		s.Person = nil
		residencyPerson = nil
	}
	out.Revision = out.Profile.Revision
	out.Residency.Own = out.Profile.Residency
	effective, err := boardResidency(ctx, tx, s, residencyPerson, project, "")
	if err != nil {
		return out, err
	}
	out.Residency.Effective = effective
	counts := map[string]int{}
	if modelprefs.Strictness(effective) > 0 {
		counts, err = c.residencyCounts(ctx, tx, project, effective)
		if err != nil {
			return out, err
		}
	}
	placed := map[string]bool{}
	for _, k := range boardColumns(s) {
		d := modelprefs.ResolveBoard(s, modelprefs.BoardQuery{Column: k.Slug, Situation: situation}, nil)
		columnState := s
		columnState.Lines = c.residencyLines(s.Lines, counts, d, effective)
		d = modelprefs.ResolveBoard(columnState, modelprefs.BoardQuery{Column: k.Slug, Situation: situation}, nil)
		col := boardColumn{Column: k.Slug, Label: k.Label, Short: k.Label, Sentence: k.Hint, Fixed: k.Slug == "other" || k.Slug == "concept" || strings.HasPrefix(k.Slug, "review:"), Hidden: slices.Contains(out.Profile.HiddenKinds, k.Slug), Source: d.Source, List: []boardCard{}, Top: []boardCard{}, Free: []boardCard{}, Bottom: []boardCard{}, Not: []boardCard{}, Cant: d.Cant}
		col.Thinking.Word, col.Thinking.Source = d.Thinking, d.ThinkingSource
		col.FollowsFirst, col.Thinking.FromColumn = d.FollowsFirst, d.ThinkingColumn
		for _, order := range s.Orders {
			if order.ProfileID == out.Profile.ID && order.Column == k.Slug && order.Situation == d.Situation {
				col.Thinking.Own = order.Thinking
				break
			}
		}
		for _, id := range d.Rank {
			card := c.card(id, d)
			placed[id] = true
			col.List = append(col.List, card)
			if slices.Contains(d.Top, id) {
				col.Top = append(col.Top, card)
			} else if slices.Contains(d.Bottom, id) {
				col.Bottom = append(col.Bottom, card)
			} else {
				col.Free = append(col.Free, card)
			}
		}
		for _, id := range d.Not {
			if _, ok := c.profiles[id]; ok {
				col.Not = append(col.Not, c.card(id, d))
				placed[id] = true
			}
		}
		for _, held := range d.Cant {
			placed[held.Line] = true
		}
		if layer == "rules" { // Rules remain visible when capability prevents execution.
			col.Top, col.Bottom = []boardCard{}, []boardCard{}
			for _, id := range d.Top {
				card := c.card(id, d)
				for _, rule := range s.Rules {
					if rule.Column == k.Slug && rule.Line == id && rule.Lock == "top" {
						card.Lock = rule.BoardLock()
						break
					}
				}
				col.Top = append(col.Top, card)
			}
			for _, id := range d.Bottom {
				card := c.card(id, d)
				for _, rule := range s.Rules {
					if rule.Column == k.Slug && rule.Line == id && rule.Lock == "bottom" {
						card.Lock = rule.BoardLock()
						break
					}
				}
				col.Bottom = append(col.Bottom, card)
			}
		}
		out.Columns = append(out.Columns, col)
	}
	for _, o := range s.Orders {
		for _, id := range o.Rank {
			placed[id] = true
		}
		for _, id := range o.Not {
			placed[id] = true
		}
	}
	for _, rule := range s.Rules {
		placed[rule.Line] = true
	}
	for _, line := range c.lines {
		if !placed[line.ID] && !slices.Contains(out.Profile.DismissedLines, line.ID) {
			out.Tray = append(out.Tray, c.card(line.ID, modelprefs.BoardDecision{Thinking: "standard", Locks: map[string]*modelprefs.BoardLock{}}))
		}
	}
	var audit []byte
	err = tx.QueryRow(ctx, `SELECT audit FROM model_pref_migrations`).Scan(&audit)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if len(audit) > 0 {
		out.NeedsYou = append(out.NeedsYou, "check_work_kind_sentences")
	}
	return out, nil
}
func (m *Module) board(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project, err := projectInput(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	layer, situation := r.URL.Query().Get("layer"), r.URL.Query().Get("situation")
	if layer == "" {
		layer = "mine"
	}
	if situation == "" {
		situation = "first"
	}
	if layer != "mine" && layer != "default" && layer != "rules" || !validBoardSituation(situation) {
		writePreferenceError(w, prefFail(400, "invalid_board_view"))
		return
	}
	if err := PrepareCatalog(r.Context(), m.pool, p, CatalogPreparation{Operation: CatalogRead, Request: r, Authorize: func(ctx context.Context, tx pgx.Tx, current tenant.Principal) (bool, error) {
		err := readableProject(ctx, tx, current, project)
		return err == nil, err
	}}); err != nil {
		writePreferenceError(w, err)
		return
	}
	var out boardDocument
	err = m.readSnapshot(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		current, err := currentModelReader(r, tx, p)
		if err != nil {
			return err
		}
		if err := readableProject(r.Context(), tx, current, project); err != nil {
			return err
		}
		person, err := currentPreferencePerson(r.Context(), tx, current)
		if err != nil {
			return err
		}
		s, err := modelprefs.LoadBoard(r.Context(), tx, person, project)
		if err != nil {
			return err
		}
		c, err := loadBoardCatalog(r.Context(), tx)
		if err != nil {
			return err
		}
		s.Lines = c.lines
		out, err = boardDocumentFor(r.Context(), tx, s, c, person, project, layer, situation)
		return err
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func validBoardSituation(s string) bool { return s == "first" || s == "fix" || s == "stuck" }
func validBoardColumn(s modelprefs.BoardState, column string) bool {
	for _, k := range boardColumns(s) {
		if column == k.Slug {
			return true
		}
	}
	return false
}
func writeBoardError(w http.ResponseWriter, err error) {
	if errors.Is(err, modelprefs.ErrBoardBounds) {
		err = prefFail(413, "model_board_limit")
	}
	writePreferenceError(w, err)
}

// The legacy surface stays readable during the one-release transition.
func (m *Module) legacyPreferencesReadOnly(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	w.Header().Set("Allow", "GET")
	writePreferenceError(w, prefFail(405, "model_preferences_read_only"))
}

// Residency routing counts are calculated with the same live authority as dispatch.
func boardRouteAccounts(ctx context.Context, tx pgx.Tx, p Profile, project, residency string, now time.Time) ([]string, error) {
	return agentaccounts.QualifyingAccountIDs(ctx, tx, p.ID, p.Harness, project, residency, now)
}
