// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type simplePick struct {
	Line    *string `json:"line"`
	Effort  *string `json:"effort"`
	Harness string  `json:"harness,omitempty"`
	Model   string  `json:"model,omitempty"`
}

func pickProfile(p *Profile) simplePick {
	if p == nil {
		return simplePick{}
	}
	line, effort := boardLineID(*p), p.Effort
	return simplePick{Line: &line, Effort: &effort, Harness: p.Harness, Model: p.Model}
}

type simpleLock struct {
	By     *string    `json:"by"`
	At     *time.Time `json:"at"`
	Reason string     `json:"reason"`
}
type simpleRow struct {
	simplePick
	Column string      `json:"column"`
	Mine   bool        `json:"mine"`
	Lock   *simpleLock `json:"lock"`
}
type simpleTrace struct {
	Role     string `json:"role,omitempty"`
	Line     string `json:"line"`
	Stage    string `json:"stage"`
	Reason   string `json:"reason"`
	Selected bool   `json:"selected"`
}
type simpleNext struct {
	simplePick
	Column   string        `json:"column"`
	Ticket   string        `json:"ticket"`
	Reviewer simplePick    `json:"reviewer"`
	Trace    []simpleTrace `json:"trace"`
}
type simpleUnavailable struct {
	Column      string  `json:"column"`
	Line        string  `json:"line"`
	RunsInstead *string `json:"runs_instead"`
	Effort      *string `json:"effort"`
	Reason      string  `json:"reason"`
	Ticket      *string `json:"ticket"`
}
type simpleNew struct {
	Line  string   `json:"line"`
	CanDo []string `json:"can_do"`
}
type simpleDocument struct {
	All        simpleRow   `json:"all"`
	Exceptions []simpleRow `json:"exceptions"`
	Reviews    struct {
		Mode string `json:"mode"`
	} `json:"reviews"`
	Next          *simpleNext         `json:"next"`
	Unavailable   []simpleUnavailable `json:"unavailable"`
	NewLines      []simpleNew         `json:"new_lines"`
	Revision      int64               `json:"revision"`
	RulesRevision int64               `json:"rules_revision"`
	PersonID      *string             `json:"person_id"`
}

func simpleRowFor(s modelprefs.BoardState, c boardCatalog, column string) simpleRow {
	d := rawBoardDecision(s, column, "first")
	row := simpleRow{Column: column}
	if s.Person != nil {
		for _, o := range s.Orders {
			if o.ProfileID == s.Person.ID && o.Column == column && o.Situation == "first" {
				row.Mine = true
			}
		}
	}
	if len(d.Rank) > 0 {
		id := d.Rank[0]
		row.Line = &id
		ps := c.effortCandidates(id, d.Effort, d.EffortLevel, false)
		if len(ps) > 0 {
			row.simplePick = pickProfile(&ps[0])
		}
		if lock := d.Locks[id]; lock != nil && lock.Value == "top" {
			row.Lock = &simpleLock{By: lock.Who, At: lock.At, Reason: lock.Why}
		}
	}
	return row
}

func simpleTraceFor(out *WorkResolution, c boardCatalog) []simpleTrace {
	trace := []simpleTrace{}
	if out == nil {
		return trace
	}
	seen := map[string]bool{}
	for _, held := range out.Trace.Held {
		stage := "column"
		if strings.HasPrefix(held.Reason, "default:") {
			stage = "default"
		}
		if strings.HasPrefix(held.Reason, "role:") {
			stage = "role"
		}
		trace = append(trace, simpleTrace{Role: out.Role, Line: held.Line, Stage: stage, Reason: held.Reason})
		seen[held.Line] = true
	}
	for _, step := range out.Ladder {
		for _, ps := range c.profiles {
			for _, p := range ps {
				if p.ID != step.ProfileID {
					continue
				}
				line := boardLineID(p)
				if !step.Selected && seen[line] {
					continue
				}
				trace = append(trace, simpleTrace{Role: out.Role, Line: line, Stage: candidateStage(step), Reason: strings.Join(step.SkipReasons, "; "), Selected: step.Selected})
				seen[line] = true
			}
		}
	}
	return trace
}

func (m *Module) simple(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	level, err := boardFor(r)
	if err != nil {
		writeBoardError(w, err)
		return
	}
	if err := PrepareCatalog(r.Context(), m.pool, p, CatalogPreparation{Operation: CatalogRead, Request: r}); err != nil {
		writeBoardError(w, err)
		return
	}
	out := simpleDocument{Exceptions: []simpleRow{}, Unavailable: []simpleUnavailable{}, NewLines: []simpleNew{}}
	err = m.readSnapshot(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		current, err := currentModelReader(r, tx, p)
		if err != nil {
			return err
		}
		person, err := currentPreferencePerson(ctx, tx, current)
		if err != nil {
			return err
		}
		out.PersonID = person
		s, err := modelprefs.LoadBoard(ctx, tx, person, "")
		if err != nil {
			return err
		}
		c, err := loadBoardCatalog(ctx, tx)
		if err != nil {
			return err
		}
		s.Lines = c.lines
		if level == "default" {
			s.Person = nil
		}
		out.Revision = targetBoardProfile(s, level).Revision
		out.RulesRevision, err = ruleRevision(ctx, tx, "workspace", "")
		if err != nil {
			return err
		}
		out.Reviews.Mode = "cross_family"
		out.All = simpleRowFor(s, c, "other")
		columns := []string{"other"}
		for _, k := range boardColumns(s) {
			if k.Slug == "other" || strings.HasPrefix(k.Slug, "review:") {
				continue
			}
			row := simpleRowFor(s, c, k.Slug)
			// An omitted kind follows Default. Show a row when this scope owns it, it is locked, or the pick
			// actually differs from that default — never because a column template disagrees with itself.
			if row.Lock != nil || row.Mine || !sameSimplePick(row.simplePick, out.All.simplePick) {
				out.Exceptions = append(out.Exceptions, row)
				columns = append(columns, k.Slug)
			}
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		for _, column := range columns {
			row := simpleRowFor(s, c, column)
			if row.Line == nil {
				continue
			}
			q := WorkQuery{Role: "build", Area: column, Column: column, Concept: column == "concept", Situation: "first", PersonID: person, WorkspaceOnly: level == "default", ExplicitBoard: true}
			resolved, err := resolveBoardWork(ctx, tx, current, q, now, nil)
			if err != nil {
				return err
			}
			chosen := simplePick{}
			if resolved != nil {
				chosen = pickProfile(resolved.Profile)
			}
			if chosen.Line == nil || *chosen.Line != *row.Line {
				reasons := []string{}
				for _, step := range simpleTraceFor(resolved, c) {
					if step.Line == *row.Line && step.Reason != "" {
						reasons = append(reasons, step.Reason)
					}
				}
				reason := strings.Join(reasons, "; ")
				if reason == "" {
					reason = "no qualified account with available capacity"
				}
				out.Unavailable = append(out.Unavailable, simpleUnavailable{Column: column, Line: *row.Line, RunsInstead: chosen.Line, Effort: chosen.Effort, Reason: reason})
			}
		}
		placed := map[string]bool{}
		for _, o := range s.Orders {
			for _, line := range o.Rank {
				placed[line] = true
			}
			for _, line := range o.Not {
				placed[line] = true
			}
		}
		for _, rule := range s.Rules {
			placed[rule.Line] = true
		}
		profile := targetBoardProfile(s, level)
		for _, line := range c.lines {
			if placed[line.ID] || slices.Contains(profile.DismissedLines, line.ID) {
				continue
			}
			ps := c.candidates(line.ID, 3, false)
			active := false
			for _, p := range ps {
				active = active || p.Enabled && !p.Retired
			}
			if !active {
				continue
			}
			// Templates are saved defaults too; they are never announced as new.
			if slices.Contains(modelprefs.TemplateRank("balanced", "concept"), line.ID) || slices.Contains(modelprefs.TemplateRank("balanced", "other"), line.ID) {
				continue
			}
			item := simpleNew{Line: line.ID, CanDo: []string{}}
			for _, k := range boardColumns(s) {
				if strings.HasPrefix(k.Slug, "review:") {
					continue
				}
				if line.Tools || k.Slug == "concept" {
					item.CanDo = append(item.CanDo, k.Slug)
				}
			}
			out.NewLines = append(out.NewLines, item)
		}
		pendingRows, err := tx.Query(ctx, `SELECT harness,model,effort FROM model_observations o WHERE NOT EXISTS(SELECT 1 FROM model_profiles p WHERE p.harness=o.harness AND p.model=o.model AND p.effort=o.effort AND p.enabled) ORDER BY harness,model,effort LIMIT 513`)
		if err != nil {
			return err
		}
		pending := []Observation{}
		for pendingRows.Next() {
			var o Observation
			if err := pendingRows.Scan(&o.Harness, &o.Model, &o.Effort); err != nil {
				pendingRows.Close()
				return err
			}
			pending = append(pending, o)
		}
		err = pendingRows.Err()
		pendingRows.Close()
		if err != nil {
			return err
		}
		if len(pending) > 512 {
			return modelprefs.ErrBoardBounds
		}
		announced := map[string]bool{}
		for _, item := range out.NewLines {
			announced[item.Line] = true
		}
		for _, o := range pending {
			pin, ok := observedPin(o)
			if !ok || KnownInvalid(o.Model) {
				continue
			}
			line := boardLineID(Profile{Harness: pin.Harness, Family: pin.Family, Model: pin.Model})
			if announced[line] || slices.Contains(profile.DismissedLines, line) {
				continue
			}
			announced[line] = true
			item := simpleNew{Line: line, CanDo: []string{}}
			for _, k := range boardColumns(s) {
				if strings.HasPrefix(k.Slug, "review:") {
					continue
				}
				if pin.Family != "xai" || k.Slug == "concept" {
					item.CanDo = append(item.CanDo, k.Slug)
				}
			}
			out.NewLines = append(out.NewLines, item)
		}
		if len(out.NewLines) > 512 {
			return modelprefs.ErrBoardBounds
		}
		out.Next, err = simpleNextFor(ctx, tx, current, person, level == "default", now, c)
		return err
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func sameSimplePick(a, b simplePick) bool {
	return (a.Line == nil && b.Line == nil || a.Line != nil && b.Line != nil && *a.Line == *b.Line) && (a.Effort == nil && b.Effort == nil || a.Effort != nil && b.Effort != nil && *a.Effort == *b.Effort)
}

func simpleNextFor(ctx context.Context, tx pgx.Tx, p tenant.Principal, person *string, workspace bool, now time.Time, c boardCatalog) (*simpleNext, error) {
	var id, key, project string
	var fields []byte
	err := tx.QueryRow(ctx, `SELECT r.queue_node_id::text,n.key,coalesce(n.project_id::text,''),CASE WHEN octet_length(n.fields::text)<=1048576 THEN n.fields END
 FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.queue_node_id
 WHERE r.status='queued' AND n.deleted_at IS NULL AND lower(btrim(n.state)) IN ('new','open','backlog')
 ORDER BY r.queue_target_agent_id NULLS FIRST,
 CASE WHEN r.queue_target_agent_id IS NOT NULL OR EXISTS(SELECT 1 FROM agent_runs m WHERE m.status='queued' AND m.queue_target_agent_id IS NULL AND m.queue_rank IS NOT NULL) THEN 0 ELSE CASE n.fields->>'priority' WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'low' THEN 3 ELSE 2 END END,
 r.queue_rank NULLS LAST,CASE WHEN r.queue_target_agent_id IS NOT NULL THEN r.queue_at END DESC,r.queue_at,r.id LIMIT 1`).Scan(&id, &key, &project, &fields)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
		return nil, nil
	}
	q := WorkQuery{TicketID: id, PersonID: person, ProjectID: project, Role: "build", ExplicitBoard: true, WorkspaceOnly: workspace, Queued: true}
	if len(fields) == 0 {
		return nil, prefFail(413, "ticket_fields_limit")
	}
	placement := modelprefs.PlacementFields(fields)
	q.Area, q.Complexity, q.TicketResidency = placement.Area, placement.Complexity, placement.Residency
	if placement.RouteRole != "" && validRole(placement.RouteRole) {
		q.Role = placement.RouteRole
	}
	q, err = boardTicketFields(ctx, tx, q, fields)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveBoardWork(ctx, tx, p, q, now, nil)
	if err != nil {
		return nil, err
	}
	if resolved == nil || resolved.Profile == nil {
		// The ticket is queued. Nothing that can run it is not an empty queue.
		out := &simpleNext{Ticket: key, Trace: []simpleTrace{}}
		if resolved != nil {
			out.Column = resolved.Trace.Column
			out.Trace = simpleTraceFor(resolved, c)
		}
		return out, nil
	}
	out := &simpleNext{simplePick: pickProfile(resolved.Profile), Column: resolved.Trace.Column, Ticket: key, Trace: simpleTraceFor(resolved, c)}
	if resolved.Profile != nil {
		q.Role, q.AuthorFamily = "review-gate", resolved.Profile.Family
		reviewer, err := resolveBoardWork(ctx, tx, p, q, now, nil)
		if err != nil {
			return nil, err
		}
		if reviewer != nil {
			out.Reviewer = pickProfile(reviewer.Profile)
			out.Trace = append(out.Trace, simpleTraceFor(reviewer, c)...)
		}
	}
	return out, nil
}

func candidateStage(c Candidate) string {
	if c.Stage != "" {
		return c.Stage
	}
	return "column"
}
