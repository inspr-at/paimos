// SPDX-License-Identifier: AGPL-3.0-only
package modelprefs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrBoardBounds = errors.New("model board input limit exceeded")

func EmptyBoardProfile(scope string, person *string) BoardProfile {
	p := BoardProfile{Scope: scope, PersonID: person, HiddenKinds: []string{}, DismissedLines: []string{}}
	if scope == "workspace" {
		template, thinking, usage := "balanced", "standard", "balanced"
		p.Template = &template
		p.Thinking = &thinking
		p.Usage = &usage
	}
	return p
}
func LoadBoard(ctx context.Context, tx pgx.Tx, person *string, project string) (BoardState, error) {
	s := BoardState{Workspace: EmptyBoardProfile("workspace", nil), Orders: []BoardOrder{}, Rules: []Rule{}, Kinds: []Kind{}, SmallHours: 2, FixRounds: 3}
	if person != nil {
		p := EmptyBoardProfile("person", person)
		s.Person = &p
	}
	rows, err := tx.Query(ctx, `SELECT id::text,scope,person_id::text,template,thinking,usage,residency,hidden_kinds,dismissed_lines,revision FROM model_pref_profiles WHERE scope='workspace' OR person_id=$1::uuid LIMIT 3`, person)
	if err != nil {
		return s, err
	}
	ids := []string{}
	for rows.Next() {
		var p BoardProfile
		if err := rows.Scan(&p.ID, &p.Scope, &p.PersonID, &p.Template, &p.Thinking, &p.Usage, &p.Residency, &p.HiddenKinds, &p.DismissedLines, &p.Revision); err != nil {
			rows.Close()
			return s, err
		}
		ids = append(ids, p.ID)
		if p.Scope == "workspace" {
			s.Workspace = p
		} else {
			s.Person = &p
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	if len(ids) > 2 {
		return s, ErrBoardBounds
	}
	rows, err = tx.Query(ctx, `SELECT profile_id::text,column_key,situation,rank,not_allowed,thinking FROM model_pref_orders WHERE profile_id=ANY($1::uuid[]) ORDER BY profile_id,column_key,situation LIMIT 1537`, ids)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var o BoardOrder
		if err := rows.Scan(&o.ProfileID, &o.Column, &o.Situation, &o.Rank, &o.Not, &o.Thinking); err != nil {
			rows.Close()
			return s, err
		}
		if len(o.Rank) > 512 || len(o.Not) > 512 {
			rows.Close()
			return s, ErrBoardBounds
		}
		s.Orders = append(s.Orders, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	if len(s.Orders) > 1536 {
		return s, ErrBoardBounds
	}
	rows, err = tx.Query(ctx, `SELECT scope,project_id::text,column_key,line,lock,position,why,set_by::text,set_at FROM model_rules WHERE scope='workspace' OR project_id=$1::uuid ORDER BY scope,position,line LIMIT 4097`, optional(project))
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.Scope, &r.ProjectID, &r.Column, &r.Line, &r.Lock, &r.Position, &r.Why, &r.SetBy, &r.SetAt); err != nil {
			rows.Close()
			return s, err
		}
		s.Rules = append(s.Rules, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	if len(s.Rules) > 4096 {
		return s, ErrBoardBounds
	}
	rows, err = tx.Query(ctx, `SELECT id::text,slug,coalesce(label_override,label),hint,project_id::text,system,position,coalesce(examples,'{}'),coalesce(labels,'{}') FROM work_kinds WHERE archived_at IS NULL AND (project_id IS NULL OR project_id=$1::uuid) ORDER BY position,slug,id LIMIT 257`, optional(project))
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var k Kind
		if err := rows.Scan(&k.ID, &k.Slug, &k.Label, &k.Hint, &k.ProjectID, &k.System, &k.Position, &k.Examples, &k.Labels); err != nil {
			rows.Close()
			return s, err
		}
		s.Kinds = append(s.Kinds, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	if len(s.Kinds) > 256 {
		return s, ErrBoardBounds
	}
	err = tx.QueryRow(ctx, `SELECT small_hours,fix_rounds FROM model_situation_limits LIMIT 1`).Scan(&s.SmallHours, &s.FixRounds)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return s, err
}

// LoadBoardRouting bypasses project response visibility only for routing metadata.
func LoadBoardRouting(ctx context.Context, tx pgx.Tx, person *string, project string) (BoardState, error) {
	var s BoardState
	err := withRoutingVisibility(ctx, tx, func() error { var err error; s, err = LoadBoard(ctx, tx, person, project); return err })
	return s, err
}

type SituationLimits struct {
	SmallHours int        `json:"small_hours"`
	FixRounds  int        `json:"fix_rounds"`
	Revision   int64      `json:"revision"`
	SetBy      *string    `json:"set_by"`
	SetAt      *time.Time `json:"set_at"`
}

func LoadSituationLimits(ctx context.Context, tx pgx.Tx) (SituationLimits, error) {
	out := SituationLimits{SmallHours: 2, FixRounds: 3}
	err := tx.QueryRow(ctx, `SELECT small_hours,fix_rounds,revision,set_by::text,set_at FROM model_situation_limits`).Scan(&out.SmallHours, &out.FixRounds, &out.Revision, &out.SetBy, &out.SetAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return out, err
}

func BoardResidencyFloor(ctx context.Context, tx pgx.Tx, person *string) (string, error) {
	floor := "any"
	rows, err := tx.Query(ctx, `SELECT residency FROM model_pref_profiles WHERE scope='workspace' OR person_id=$1::uuid LIMIT 3`, person)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var value *string
		if err := rows.Scan(&value); err != nil {
			return "", err
		}
		if value != nil {
			floor = Strictest(floor, *value)
		}
	}
	if count > 2 {
		return "", ErrBoardBounds
	}
	return floor, rows.Err()
}
