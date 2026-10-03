// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

// BuildSettings is the closed, bounded shape shared by defaults and overrides.
// Null and omitted values inherit; lifecycle edits never start spending.
type BuildSettings struct {
	Window             *BuildWindow `json:"window,omitempty"`
	BudgetAgentHours   *float64     `json:"budget_agent_hours,omitempty"`
	MaxAgents          *int         `json:"max_agents,omitempty"`
	LargestTicketHours *float64     `json:"largest_ticket_hours,omitempty"`
}
type BuildWindow struct {
	Timezone string       `json:"timezone"`
	Slots    []WindowSlot `json:"slots"`
}
type WindowSlot struct {
	Days []int  `json:"days"`
	From string `json:"from"`
	To   string `json:"to"`
}

func ParseBuildSettings(raw []byte) (BuildSettings, error) {
	var s BuildSettings
	if len(raw) == 0 || len(raw) > 2048 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return s, errors.New("build settings must be an object of at most 2 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, errors.New("invalid build settings")
	}
	if d.Decode(new(any)) != io.EOF {
		return s, errors.New("invalid build settings")
	}
	for _, v := range []*float64{s.BudgetAgentHours, s.LargestTicketHours} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v <= 0 || *v > 1000000) {
			return s, errors.New("invalid positive hours limit")
		}
	}
	if s.MaxAgents != nil && (*s.MaxAgents < 1 || *s.MaxAgents > 1000) {
		return s, errors.New("invalid maximum agents")
	}
	if s.Window != nil {
		if _, err := windowMinutes(s.Window); err != nil {
			return s, err
		}
	}
	return s, nil
}
func clockMinute(s string) (int, error) {
	if len(s) != 5 {
		return 0, errors.New("window times must be HH:MM")
	}
	t, err := time.Parse("15:04", s)
	return t.Hour()*60 + t.Minute(), err
}
func windowMinutes(w *BuildWindow) ([10080]bool, error) {
	var out [10080]bool
	if len(w.Timezone) == 0 || len(w.Timezone) > 128 || len(w.Slots) > 32 || w.Slots == nil {
		return out, errors.New("invalid build window")
	}
	if _, err := time.LoadLocation(w.Timezone); err != nil {
		return out, errors.New("invalid window timezone")
	}
	for _, s := range w.Slots {
		from, err := clockMinute(s.From)
		if err != nil {
			return out, err
		}
		to, err := clockMinute(s.To)
		if err != nil {
			return out, err
		}
		if from == to || len(s.Days) < 1 || len(s.Days) > 7 {
			return out, errors.New("invalid window slot")
		}
		seen := map[int]bool{}
		duration := (to - from + 1440) % 1440
		for _, day := range s.Days {
			if day < 0 || day > 6 || seen[day] {
				return out, errors.New("invalid or duplicate window day")
			}
			seen[day] = true
			for m := 0; m < duration; m++ {
				out[(day*1440+from+m)%10080] = true
			}
		}
	}
	return out, nil
}
func ResolveBuildSettings(defaults, overrides BuildSettings) (BuildSettings, map[string]string) {
	out := defaults
	sources := map[string]string{}
	choose := func(name string, base, override bool) {
		sources[name] = "missing"
		if base {
			sources[name] = "inherited"
		}
		if override {
			sources[name] = "overridden"
		}
	}
	choose("window", defaults.Window != nil, overrides.Window != nil)
	if overrides.Window != nil {
		out.Window = overrides.Window
	}
	choose("budget_agent_hours", defaults.BudgetAgentHours != nil, overrides.BudgetAgentHours != nil)
	if overrides.BudgetAgentHours != nil {
		out.BudgetAgentHours = overrides.BudgetAgentHours
	}
	choose("max_agents", defaults.MaxAgents != nil, overrides.MaxAgents != nil)
	if overrides.MaxAgents != nil {
		out.MaxAgents = overrides.MaxAgents
	}
	choose("largest_ticket_hours", defaults.LargestTicketHours != nil, overrides.LargestTicketHours != nil)
	if overrides.LargestTicketHours != nil {
		out.LargestTicketHours = overrides.LargestTicketHours
	}
	return out, sources
}
func Tightens(defaults, override BuildSettings) bool {
	if override.BudgetAgentHours != nil && (defaults.BudgetAgentHours == nil || *override.BudgetAgentHours > *defaults.BudgetAgentHours) {
		return false
	}
	if override.MaxAgents != nil && defaults.MaxAgents != nil && *override.MaxAgents > *defaults.MaxAgents {
		return false
	}
	if override.LargestTicketHours != nil && defaults.LargestTicketHours != nil && *override.LargestTicketHours > *defaults.LargestTicketHours {
		return false
	}
	if override.Window != nil {
		if defaults.Window == nil || defaults.Window.Timezone != override.Window.Timezone {
			return false
		}
		base, e1 := windowMinutes(defaults.Window)
		child, e2 := windowMinutes(override.Window)
		if e1 != nil || e2 != nil {
			return false
		}
		for i, v := range child {
			if v && !base[i] {
				return false
			}
		}
	}
	return true
}

type UpdateRequest struct {
	ProjectID, ReleaseID    string
	ExpectedRevision        int64
	Title, Body, Visibility *string
	EntryClosesAt           *time.Time
	SetEntryDeadline        bool
	BuildSettings           json.RawMessage
}

func (s *Store) Update(ctx context.Context, p tenant.Principal, in UpdateRequest) (Release, error) {
	if !uuid(in.ReleaseID) || in.ExpectedRevision < 1 || in.Title != nil && (len(*in.Title) > 512 || strings.TrimSpace(*in.Title) == "") || in.Body != nil && len(*in.Body) > 65536 {
		return Release{}, errors.New("invalid release edit")
	}
	var settings BuildSettings
	var err error
	if in.BuildSettings != nil {
		settings, err = ParseBuildSettings(in.BuildSettings)
		if err != nil {
			return Release{}, err
		}
	}
	var out Release
	err = s.mutate(ctx, p, in.ProjectID, "releases.write", true, func(w *write) error {
		locked, err := w.lockReleases([]string{in.ReleaseID})
		if err != nil {
			return err
		}
		old := locked[in.ReleaseID]
		if old.Revision != in.ExpectedRevision {
			return ErrRevisionChanged
		}
		if in.SetEntryDeadline || in.Visibility != nil {
			if old.State != "planned" {
				return ErrTransition
			}
		}
		rank, seq, visibility := old.Rank, old.Sequence, old.Visibility
		if in.Visibility != nil && *in.Visibility != visibility {
			if *in.Visibility != "published" || visibility != "internal" {
				return ErrTransition
			}
			lower, _, e := PublishedBounds(w.ctx, w.tx, p.TenantID, w.project, w.nextSequence)
			if e != nil {
				return e
			}
			anchor := ""
			if lower != nil {
				anchor = lower.Rank
			}
			n, e := ReleaseNeighbours(w.ctx, w.tx, p.TenantID, w.project, anchor, old.ID)
			if e != nil {
				return e
			}
			upper := ""
			if n.Next != nil {
				upper = n.Next.Rank
			}
			rank, e = Between(anchor, upper)
			if e != nil {
				return e
			}
			if w.nextSequence == 2147483647 {
				return ErrOpenReleaseCapacity
			}
			seq = w.nextSequence
			visibility = "published"
			if _, e = w.tx.Exec(w.ctx, `UPDATE project_delivery SET next_sequence=next_sequence+1,revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2`, p.TenantID, w.project); e != nil {
				return e
			}
		}
		var defaultsRaw []byte
		if err = w.tx.QueryRow(w.ctx, `SELECT build_defaults FROM project_delivery WHERE tenant_id=$1 AND project_node_id=$2`, p.TenantID, w.project).Scan(&defaultsRaw); err != nil {
			return err
		}
		if in.BuildSettings != nil {
			defaults, e := ParseBuildSettings(defaultsRaw)
			if e != nil {
				return e
			}
			if !Tightens(defaults, settings) {
				if e = w.require("releases.deploy"); e != nil {
					return e
				}
			}
		}
		var raw any
		if in.BuildSettings != nil {
			raw = in.BuildSettings
		}
		out, err = scanRelease(w.tx.QueryRow(w.ctx, `UPDATE project_releases SET visibility=$4,sequence=nullif($5,0),rank=$6,entry_closes_at=CASE WHEN $7 THEN $8::timestamptz ELSE entry_closes_at END,build_settings=coalesce($9::jsonb,build_settings),revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 AND revision=$10 RETURNING `+releaseColumns, p.TenantID, w.project, old.ID, visibility, seq, rank, in.SetEntryDeadline, in.EntryClosesAt, raw, old.Revision))
		if err != nil {
			return err
		}
		if in.Title != nil || in.Body != nil {
			if _, err = w.tx.Exec(w.ctx, `UPDATE nodes SET title=coalesce($3,title),body=coalesce($4,body),updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE tenant_id=$1 AND id=$2`, p.TenantID, old.ID, in.Title, in.Body); err != nil {
				return err
			}
		}
		w.audit("release.updated", old.ID, old, map[string]any{"release": out, "title": in.Title, "body": in.Body, "build_settings": in.BuildSettings})
		return nil
	})
	return out, err
}
func (s *Store) SetDefaults(ctx context.Context, p tenant.Principal, project string, revision int64, raw []byte) error {
	if revision < 1 {
		return errors.New("expected revision required")
	}
	if _, err := ParseBuildSettings(raw); err != nil {
		return err
	}
	return s.mutate(ctx, p, project, "releases.deploy", true, func(w *write) error {
		var before []byte
		if err := w.tx.QueryRow(w.ctx, `SELECT build_defaults FROM project_delivery WHERE tenant_id=$1 AND project_node_id=$2 AND revision=$3`, p.TenantID, project, revision).Scan(&before); err != nil {
			return ErrRevisionChanged
		}
		tag, err := w.tx.Exec(w.ctx, `UPDATE project_delivery SET build_defaults=$3,revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND revision=$4`, p.TenantID, project, raw, revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrRevisionChanged
		}
		w.audit("delivery.defaults_changed", project, json.RawMessage(before), json.RawMessage(raw))
		return nil
	})
}
